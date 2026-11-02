/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package secadmin

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// Severity of a doctor finding.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Finding is one problem found by Doctor.
type Finding struct {
	Severity Severity
	Message  string
}

func (f Finding) String() string { return string(f.Severity) + ": " + f.Message }

// Doctor checks the directory: permissions, key material, the generation chain,
// the source, and drift between the source and the installed policy. When
// instanceRoot is not empty it also checks that granted contexts and capacities
// exist in that instance.
func (d *Dir) Doctor(instanceRoot string) []Finding {
	var findings []Finding
	add := func(severity Severity, format string, args ...any) {
		findings = append(findings, Finding{severity, fmt.Sprintf(format, args...)})
	}

	d.checkModes(add)

	generations, err := d.Generations()
	if err != nil {
		add(SeverityError, "generations: %v", err)
	}
	for index, generation := range generations {
		if generation.Revision != uint64(index+1) {
			add(SeverityError, "generation %s has revision %d; revisions must be contiguous from 1", generation.Name, generation.Revision)
		}
		if generation.Name != generationName(generation.Revision) {
			add(SeverityWarning, "generation %s does not carry the name %s of its revision", generation.Name, generationName(generation.Revision))
		}
		if index > 0 && generation.Decoded.PreviousPolicyHash() != generations[index-1].Hash {
			add(SeverityError, "generation %s does not follow generation %s (previous_policy_hash)", generation.Name, generations[index-1].Name)
		}
	}
	if len(generations) == 0 {
		add(SeverityError, "no generation is installed: run init, or apply --signed in strict mode")
	}

	active, err := d.ActiveGeneration()
	switch {
	case err != nil:
		add(SeverityError, "activation state: %v", err)
	case active == nil:
		add(SeverityWarning, "the engine has not activated any generation yet")
	case len(generations) > 0 && active.Revision < generations[len(generations)-1].Revision:
		add(SeverityWarning, "generation %s is installed but the engine runs %s: reload the engine", generations[len(generations)-1].Name, active.Name)
	}

	if len(generations) > 0 {
		head := generations[len(generations)-1]
		d.checkKeys(head, add)
	}

	src, err := d.LoadSource()
	if err != nil {
		add(SeverityError, "%v", err)
		return sortFindings(findings)
	}
	compiled, err := d.Compile()
	if err != nil {
		add(SeverityError, "the source does not compile: %v", err)
		return sortFindings(findings)
	}
	for _, warning := range compiled.Result.Warnings {
		add(SeverityWarning, "%s", warning)
	}
	if compiled.Head != nil && !sameAccess(compiled.Head.Policy, compiled.Result.Policy) {
		add(SeverityWarning, "the source differs from the installed policy: run diff, then apply")
	}
	if instanceRoot != "" {
		checkTargets(src, instanceRoot, add)
	}
	return sortFindings(findings)
}

func sortFindings(findings []Finding) []Finding {
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Severity < findings[j].Severity })
	return findings
}

func (d *Dir) checkModes(add func(Severity, string, ...any)) {
	_ = filepath.Walk(d.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(d.Root, p)
		if strings.HasPrefix(filepath.Base(p), ".tmp-") {
			add(SeverityWarning, "leftover temporary file %s", rel)
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			add(SeverityError, "%s is a symbolic link; the engine refuses links in the security directory", rel)
		} else if problem := accessProblem(p, rel, info); problem != "" {
			add(SeverityError, "%s", problem)
		}
		if rel == MessageKeyFile || rel == AuthorityKeyFile {
			if info.Mode().Perm()&0o200 != 0 {
				add(SeverityError, "%s is writable; the engine requires read-only key files (0400)", rel)
			}
		}
		return nil
	})
}

func (d *Dir) checkKeys(head Generation, add func(Severity, string, ...any)) {
	document, authority, err := d.loadBootstrap(d.bootstrapRel(head.Name))
	if err != nil {
		add(SeverityError, "bootstrap of %s: %v", head.Name, err)
		return
	}
	if document.InstanceID != head.Policy.InstanceID {
		add(SeverityError, "bootstrap instance %q differs from the policy instance %q", document.InstanceID, head.Policy.InstanceID)
	}
	if _, statErr := os.Stat(d.abs(AuthorityKeyFile)); statErr == nil {
		if _, err := d.loadAuthorityPrivate(authority.KeyID); err != nil {
			add(SeverityError, "authority key: %v", err)
		}
	} else {
		add(SeverityWarning, "no authority key on this machine (strict mode): policies are signed offline")
	}
	for _, signerDefinition := range document.MessageSigners {
		content, err := d.readProtected(signerDefinition.Config.KeyFile, 16<<10)
		if err != nil {
			add(SeverityError, "message signer %q: key file unavailable or unsafe", signerDefinition.Name)
			continue
		}
		private, err := ParsePrivateKeyPEM(content)
		if err != nil {
			add(SeverityError, "message signer %q: %v", signerDefinition.Name, err)
			continue
		}
		id, _ := trust.SigningKeyID(private.Public().(ed25519.PublicKey))
		shared.ZeroBytes(private)
		if id != signerDefinition.ExpectedKeyID {
			add(SeverityError, "message signer %q: the key file does not match expected_key_id", signerDefinition.Name)
		}
	}
}

// checkTargets warns about grants on contexts or capacities absent from the
// instance. A context path /root/a/b is the directory <root>/a/b.
func checkTargets(src *policysrc.Source, root string, add func(Severity, string, ...any)) {
	contextDir := func(contextPath string) string {
		if contextPath == "/root" {
			return root
		}
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(contextPath, "/root/")))
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(src.Principals))
	for id := range src.Principals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for rank, g := range src.Principals[id].Grants {
			verb, targets, _ := g.Verb()
			for _, raw := range targets {
				t, err := policysrc.ParseTarget(raw)
				if err != nil {
					continue
				}
				key := id + "|" + raw
				if seen[key] {
					continue
				}
				seen[key] = true
				info, err := os.Stat(contextDir(t.Path))
				if err != nil || !info.IsDir() {
					add(SeverityWarning, "principal %q grant %d (%s %s): context %s does not exist in the instance", id, rank+1, verb, raw, t.Path)
					continue
				}
				if verb == policysrc.VerbCall && t.Named() {
					if _, err := os.Stat(filepath.Join(contextDir(t.Path), "capacity", t.Name+".json")); err != nil {
						add(SeverityWarning, "principal %q grant %d (call %s): capacity %q does not exist in %s", id, rank+1, raw, t.Name, t.Path)
					}
				}
			}
		}
	}
}
