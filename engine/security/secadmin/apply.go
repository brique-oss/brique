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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"brique_engine/security/catalog"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// Compiled is the access source compiled against the head generation.
type Compiled struct {
	Result *policysrc.Result
	// Head is the latest installed generation; nil before the first one.
	Head     *Generation
	Revision uint64
}

func offlineCatalog() trust.Catalog {
	return trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: catalog.ResourceActions()}
}

// Compile compiles the source as the successor of the latest generation.
func (d *Dir) Compile() (*Compiled, error) {
	src, err := d.LoadSource()
	if err != nil {
		return nil, err
	}
	generations, err := d.Generations()
	if err != nil {
		return nil, err
	}
	options := policysrc.Options{Revision: "1", IssuedAt: d.now().Format("2006-01-02T15:04:05Z")}
	out := &Compiled{Revision: 1}
	if len(generations) > 0 {
		head := generations[len(generations)-1]
		out.Head = &head
		out.Revision = head.Revision + 1
		options.Revision = strconv.FormatUint(out.Revision, 10)
		options.PreviousPolicyHash = head.Hash
		if src.InstanceID != head.Policy.InstanceID || src.PolicyID != head.Policy.PolicyID {
			return nil, fmt.Errorf("the source is bound to %s/%s but the installed policy to %s/%s",
				src.InstanceID, src.PolicyID, head.Policy.InstanceID, head.Policy.PolicyID)
		}
	}
	result, err := policysrc.Compile(src, offlineCatalog(), options)
	if err != nil {
		return nil, err
	}
	out.Result = result
	return out, nil
}

// ApplyOptions configures Apply.
type ApplyOptions struct {
	// Reload asks the running engine to adopt the new generation (SIGHUP).
	Reload bool
	// Force installs a generation even if nothing changed.
	Force bool
	// SignedFile is an envelope signed offline (strict mode).
	SignedFile string
}

// ApplyResult reports what Apply did.
type ApplyResult struct {
	Generation string
	Revision   uint64
	Hash       string
	Unchanged  bool
	Warnings   []string
	Reloaded   bool
	ReloadNote string
}

// Apply compiles the source, checks that the engine would accept the successor,
// signs it (solo) or verifies the offline signature (strict), installs it as a
// new generation atomically and optionally signals the engine.
func (d *Dir) Apply(options ApplyOptions) (*ApplyResult, error) {
	compiled, err := d.Compile()
	if err != nil {
		return nil, err
	}
	result := &ApplyResult{Warnings: compiled.Result.Warnings}
	if compiled.Head != nil && !options.Force && sameAccess(compiled.Head.Policy, compiled.Result.Policy) {
		result.Unchanged = true
		result.Generation, result.Revision, result.Hash = compiled.Head.Name, compiled.Head.Revision, compiled.Head.Hash
		return result, nil
	}

	headName := ""
	if compiled.Head != nil {
		headName = compiled.Head.Name
	}
	bootstrapRel := d.bootstrapRel(headName)
	bootstrapBytes, err := d.readProtected(bootstrapRel, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("bootstrap unavailable: %w", err)
	}
	_, authority, err := d.loadBootstrap(bootstrapRel)
	if err != nil {
		return nil, err
	}
	var envelope []byte
	if options.SignedFile != "" {
		envelope, err = os.ReadFile(options.SignedFile)
		if err != nil {
			return nil, fmt.Errorf("signed policy unavailable: %w", err)
		}
	} else {
		envelope, err = d.sign(compiled.Result.Policy, authority)
		if err != nil {
			return nil, err
		}
	}
	decoded, err := trust.VerifyAndDecode(envelope, authority)
	if err != nil {
		return nil, fmt.Errorf("signed policy rejected: %w", err)
	}
	var installed trust.Policy
	if err := json.Unmarshal(decoded.CanonicalBytes(), &installed); err != nil {
		return nil, err
	}
	if options.SignedFile != "" {
		if installed.Revision != strconv.FormatUint(compiled.Revision, 10) {
			return nil, fmt.Errorf("the signed policy has revision %s, expected %d", installed.Revision, compiled.Revision)
		}
		if compiled.Head != nil && installed.PreviousPolicyHash != compiled.Head.Hash {
			return nil, fmt.Errorf("the signed policy does not follow the installed generation")
		}
		if !sameAccess(installed, compiled.Result.Policy) {
			return nil, fmt.Errorf("the signed policy does not match the access source: compile again, sign the result")
		}
	}
	if err := preflight(compiled.Head, decoded); err != nil {
		return nil, fmt.Errorf("the engine would refuse this policy: %w", err)
	}

	name := generationName(compiled.Revision)
	if err := d.install(name, bootstrapBytes, envelope); err != nil {
		return nil, err
	}
	result.Generation, result.Revision, result.Hash = name, compiled.Revision, decoded.PolicyHash()
	// The engine adopts exactly one successor per reload, so generations that
	// were installed but never adopted need one reload each.
	if active, err := d.ActiveGeneration(); err == nil && active != nil && compiled.Revision > active.Revision+1 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d generations are waiting to be adopted (the engine runs %s): the engine takes one per reload, so reload it %d times",
			compiled.Revision-active.Revision, active.Name, compiled.Revision-active.Revision))
	}
	if options.Reload {
		result.Reloaded, result.ReloadNote = d.signalEngine()
	}
	return result, nil
}

// sign produces the envelope of a compiled policy with the authority key.
func (d *Dir) sign(policy trust.Policy, authority trust.Authority) ([]byte, error) {
	private, err := d.loadAuthorityPrivate(authority.KeyID)
	if err != nil {
		return nil, err
	}
	defer shared.ZeroBytes(private)
	return SignPolicy(policy, authority.KeyID, private)
}

// preflight replays the engine's acceptance rules on a throwaway runtime: the
// candidate must be the exact successor of the head and respect tombstones.
func preflight(head *Generation, candidate *trust.DecodeResult) error {
	runtime, err := trust.NewRuntime(candidate.InstanceID(), candidate.PolicyID())
	if err != nil {
		return err
	}
	if head != nil {
		if err := runtime.Recover(head.Decoded, offlineCatalog(), trust.ActivationRecord{
			InstanceID: head.Decoded.InstanceID(), PolicyID: head.Decoded.PolicyID(), Revision: head.Decoded.Revision(),
			PolicyHash: head.Decoded.PolicyHash(), PreviousPolicyHash: head.Decoded.PreviousPolicyHash(),
		}); err != nil {
			return fmt.Errorf("installed generation %s: %w", head.Name, err)
		}
	}
	_, err = runtime.Prepare(candidate, offlineCatalog())
	return err
}

// install publishes a generation with one rename so the engine never sees a
// partial one.
func (d *Dir) install(name string, bootstrapBytes, envelope []byte) error {
	if d.Carrying() {
		return d.installInInstance(name, envelope)
	}
	staging := filepath.Join("staging", name)
	_ = os.RemoveAll(d.abs(staging))
	defer os.RemoveAll(d.abs(staging))
	if err := d.writeFile(path.Join("staging", name, BootstrapFile), bootstrapBytes, 0o600); err != nil {
		return err
	}
	if err := d.writeFile(path.Join("staging", name, PolicyFile), envelope, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(d.abs(GenerationsDir), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(d.abs(path.Join(GenerationsDir, name))); err == nil {
		return fmt.Errorf("generation %s already exists", name)
	}
	return os.Rename(d.abs(staging), d.abs(path.Join(GenerationsDir, name)))
}

// installInInstance publishes the signed policy of a generation in the instance's security
// folder with one rename. The machine's bootstrap does not change with a generation.
func (d *Dir) installInInstance(name string, envelope []byte) error {
	staging := path.Join(".staging", name)
	_ = os.RemoveAll(d.generationsAbs(staging))
	defer os.RemoveAll(d.generationsAbs(".staging"))
	if err := shared.WriteInstanceFile(d.PolicyRoot, path.Join(staging, PolicyFile), envelope, 0o644); err != nil {
		return err
	}
	if err := shared.WriteInstanceFile(d.PolicyRoot, path.Join(GenerationsDir, ".keep"), nil, 0o644); err != nil {
		return err
	}
	_ = os.Remove(d.generationsAbs(path.Join(GenerationsDir, ".keep")))
	if _, err := os.Stat(d.generationsAbs(path.Join(GenerationsDir, name))); err == nil {
		return fmt.Errorf("generation %s already exists", name)
	}
	return os.Rename(d.generationsAbs(staging), d.generationsAbs(path.Join(GenerationsDir, name)))
}

// SignPolicy signs a policy document with an authority key and returns the
// envelope. It is used by apply in solo mode and by the offline `sign` command.
func SignPolicy(policy trust.Policy, authorityKeyID string, private []byte) ([]byte, error) {
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	canonical, err := trust.CanonicalPolicy(raw)
	if err != nil {
		return nil, err
	}
	signature, err := trust.SignPolicy(canonical, private)
	if err != nil {
		return nil, err
	}
	return json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authorityKeyID, Algorithm: trust.SigningAlgorithm, Signature: signature}},
	})
}

// ActiveGeneration returns the generation the engine last activated, if the
// activation state exists.
func (d *Dir) ActiveGeneration() (*Generation, error) {
	if _, err := os.Stat(d.abs("activation-state.json")); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	content, err := d.readProtected("activation-state.json", 16<<10)
	if err != nil {
		return nil, err
	}
	var state struct {
		ActiveGeneration string `json:"active_generation"`
	}
	if err := json.Unmarshal(content, &state); err != nil || state.ActiveGeneration == "" {
		return nil, fmt.Errorf("activation state is unreadable")
	}
	generation, err := d.LoadGeneration(state.ActiveGeneration)
	if err != nil {
		return nil, err
	}
	return &generation, nil
}
