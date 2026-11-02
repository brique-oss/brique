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

// Package secadmin implements the offline administration of the protected
// security directory: it edits the access source, compiles it with
// security/policysrc, signs and installs a new generation, and checks the
// directory. It contains no authorization logic and never runs inside the
// engine (SECURITY.md §11).
package secadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"brique_engine/security/bootstrap"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// Layout of the security directory, relative to its root.
const (
	SourceFile = "source/access.json"
	// InstanceSourceFile is the access source inside the instance's security folder.
	InstanceSourceFile = "access.json"
	GenerationsDir     = "generations"
	PolicyFile         = "trust-policy.json"
	BootstrapFile      = "bootstrap.json"
	MessageKeyFile     = "keys/message-signer.pem"
	AuthorityKeyFile   = "authority/policy-authority.pem"
	PIDFile            = "engine.pid"
	PolicyID           = "main"

	maxSourceBytes = 4 << 20
	maxGenerations = 128
)

var generationPattern = regexp.MustCompile(`^g[0-9]{6}$`)

// Dir is one security directory. Root is the machine's part: keys, the trust anchor
// (bootstrap), durable state, the denial journal. With PolicyRoot empty it holds everything,
// as it always did.
//
// With PolicyRoot set — the "security" folder of an instance — the access source lives there
// and travels with the instance. The signed generations then stay on the machine (the
// default: each machine compiles and signs the source it finds, with an authority of its
// own) unless Carry is set, in which case they travel too and the machine verifies them
// against an authority it was told to trust.
type Dir struct {
	Root string
	// PolicyRoot is <instance>/security: access.json, and with Carry the signed generations.
	PolicyRoot string
	// Carry keeps the signed generations in the instance, next to a single machine
	// bootstrap that names the trust anchor.
	Carry bool
	// Now is the clock used to date policies; time.Now when nil.
	Now func() time.Time
}

func (d *Dir) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// InstanceSource reports whether the access source lives in the instance.
func (d *Dir) InstanceSource() bool { return d.PolicyRoot != "" }

// Carrying reports whether the signed generations live in the instance.
func (d *Dir) Carrying() bool { return d.PolicyRoot != "" && d.Carry }

// sourceRel is the access source, relative to the side that holds it.
func (d *Dir) sourceRel() string {
	if d.InstanceSource() {
		return InstanceSourceFile
	}
	return SourceFile
}

// bootstrapRel is the bootstrap that applies to a generation: the machine's single
// bootstrap when the generations are carried, the generation's own otherwise. An empty name
// asks for the template a first generation starts from.
func (d *Dir) bootstrapRel(generation string) string {
	switch {
	case d.Carrying():
		return BootstrapFile
	case generation == "":
		return path.Join("source", "bootstrap.template.json")
	default:
		return path.Join(GenerationsDir, generation, BootstrapFile)
	}
}

// generationsAbs is a path among the signed generations.
func (d *Dir) generationsAbs(rel string) string {
	if d.Carrying() {
		return filepath.Join(d.PolicyRoot, filepath.FromSlash(rel))
	}
	return d.abs(rel)
}

func (d *Dir) readGenerationFile(rel string, max int64) ([]byte, error) {
	if d.Carrying() {
		return shared.ReadInstanceFile(d.PolicyRoot, rel, max)
	}
	return d.readProtected(rel, max)
}

func (d *Dir) readSource(max int64) ([]byte, error) {
	if d.InstanceSource() {
		return shared.ReadInstanceFile(d.PolicyRoot, InstanceSourceFile, max)
	}
	return d.readProtected(SourceFile, max)
}

func (d *Dir) writeSource(content []byte) error {
	if d.InstanceSource() {
		return shared.WriteInstanceFile(d.PolicyRoot, InstanceSourceFile, content, 0o644)
	}
	return d.writeFile(SourceFile, content, 0o600)
}

func (d *Dir) abs(rel string) string { return filepath.Join(d.Root, filepath.FromSlash(rel)) }

// readProtected reads a file through the same no-follow, owner and mode checks
// the engine applies.
func (d *Dir) readProtected(rel string, max int64) ([]byte, error) {
	return shared.ReadProtectedFile(d.Root, rel, shared.ProtectedFileOptions{MaxBytes: max})
}

// writeFile atomically replaces a file, creating owner-only parent directories.
func (d *Dir) writeFile(rel string, content []byte, mode os.FileMode) error {
	return shared.WriteProtectedFile(d.Root, rel, content, mode)
}

// LoadSource reads and strictly decodes the access source.
func (d *Dir) LoadSource() (*policysrc.Source, error) {
	content, err := d.readSource(maxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("access source unavailable: %w", err)
	}
	return policysrc.Decode(content)
}

// SaveSource validates then writes the access source. An invalid source is never
// written.
func (d *Dir) SaveSource(src *policysrc.Source) error {
	if err := policysrc.Validate(src); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	return d.writeSource(append(encoded, '\n'))
}

// Generation is one installed, verified policy generation.
type Generation struct {
	Name     string
	Revision uint64
	Hash     string
	Decoded  *trust.DecodeResult
	Policy   trust.Policy
}

// Generations lists the installed generations in revision order, each verified
// against the authority recorded in its own bootstrap.json.
func (d *Dir) Generations() ([]Generation, error) {
	entries, err := os.ReadDir(d.generationsAbs(GenerationsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || len(entries) > maxGenerations {
		return nil, fmt.Errorf("generations are unavailable or unbounded")
	}
	var out []Generation
	for _, entry := range entries {
		if !entry.IsDir() || !generationPattern.MatchString(entry.Name()) {
			continue
		}
		generation, err := d.LoadGeneration(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("generation %s: %w", entry.Name(), err)
		}
		out = append(out, generation)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	return out, nil
}

// LoadGeneration reads, verifies and decodes one generation.
func (d *Dir) LoadGeneration(name string) (Generation, error) {
	if !generationPattern.MatchString(name) {
		return Generation{}, fmt.Errorf("invalid generation name")
	}
	document, authority, err := d.loadBootstrap(d.bootstrapRel(name))
	if err != nil {
		return Generation{}, err
	}
	_ = document
	content, err := d.readGenerationFile(path.Join(GenerationsDir, name, PolicyFile), trust.MaxEnvelopeBytes)
	if err != nil {
		return Generation{}, err
	}
	decoded, err := trust.VerifyAndDecode(content, authority)
	if err != nil {
		return Generation{}, err
	}
	var policy trust.Policy
	if err := json.Unmarshal(decoded.CanonicalBytes(), &policy); err != nil {
		return Generation{}, err
	}
	var revision uint64
	if _, err := fmt.Sscanf(decoded.Revision(), "%d", &revision); err != nil {
		return Generation{}, err
	}
	return Generation{Name: name, Revision: revision, Hash: decoded.PolicyHash(), Decoded: decoded, Policy: policy}, nil
}

func (d *Dir) loadBootstrap(rel string) (*bootstrap.Document, trust.Authority, error) {
	content, err := d.readProtected(rel, 64<<10)
	if err != nil {
		return nil, trust.Authority{}, err
	}
	var document bootstrap.Document
	if err := trust.StrictDecodeJSON(content, &document); err != nil {
		return nil, trust.Authority{}, err
	}
	if len(document.PolicyAuthorities) != 1 {
		return nil, trust.Authority{}, fmt.Errorf("bootstrap must name exactly one policy authority")
	}
	definition := document.PolicyAuthorities[0]
	publicKey, err := decodeKey(definition.PublicKey)
	if err != nil {
		return nil, trust.Authority{}, err
	}
	return &document, trust.Authority{KeyID: definition.KeyID, PublicKey: publicKey}, nil
}

func generationName(revision uint64) string { return fmt.Sprintf("g%06d", revision) }
