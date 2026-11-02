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
	"brique_engine/security/hostdir"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"brique_engine/security/bootstrap"
	"brique_engine/security/policysrc"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// InitOptions configures a new security directory.
type InitOptions struct {
	InstanceID string
	// Strict leaves the policy authority private key off the machine:
	// AuthorityPublic is required and policies are signed offline.
	Strict          bool
	AuthorityPublic string
}

// InitResult summarizes what init created.
type InitResult struct {
	Generation     string
	MessageKeyID   string
	AuthorityKeyID string
	Strict         bool
}

// Init creates the directory, the instance message-signing key, the policy
// authority (a protected key file in solo mode, a public key in strict mode),
// the bootstrap and the first generation: revision 1, no principal.
//
// When the instance already carries its access source (a checkout on a new machine), init
// keeps it and creates only what belongs to the machine: a new message key and an authority.
// By default the authority is the machine's own — the source found is compiled and signed
// here, so nothing has to be remembered from the previous machine. An instance that carries
// signed generations (Carry) is a stricter arrangement: the machine is told which authority
// to trust with --strict --authority-public and keeps the policy as it is.
func (d *Dir) Init(options InitOptions) (*InitResult, error) {
	if d == nil || d.Root == "" {
		return nil, fmt.Errorf("security directory is required")
	}
	adopt := false
	if options.InstanceID == hostdir.TemplateInstanceID {
		return nil, fmt.Errorf("%q is the name of the template: give the instance a name of its own", hostdir.TemplateInstanceID)
	}
	if d.InstanceSource() {
		if src, err := d.LoadSource(); err == nil && src.InstanceID == hostdir.TemplateInstanceID {
			// An instance made from the template carries an empty source under the template's name:
			// giving it a name is all init has to do to it.
			if options.InstanceID == "" {
				return nil, fmt.Errorf("this instance still carries the name of the template: choose its name with --instance")
			}
			if len(src.Principals) != 0 || len(src.Peers) != 0 {
				return nil, fmt.Errorf("the source carries the template's name but is not empty: rename it by hand")
			}
			src.InstanceID = options.InstanceID
			if err := d.SaveSource(src); err != nil {
				return nil, err
			}
		}
		if src, err := d.LoadSource(); err == nil {
			adopt = true
			if options.InstanceID == "" {
				options.InstanceID = src.InstanceID
			} else if options.InstanceID != src.InstanceID {
				return nil, fmt.Errorf("the instance already carries a policy for %q, not %q", src.InstanceID, options.InstanceID)
			}
			if entries, err := os.ReadDir(filepath.Join(d.PolicyRoot, GenerationsDir)); err == nil && len(entries) > 0 && !d.Carrying() {
				return nil, fmt.Errorf("the instance carries signed generations: give the key that signed them with --strict --authority-public")
			}
			if d.Carrying() && !options.Strict {
				return nil, fmt.Errorf("the instance carries signed generations: give the key that signed them with --strict --authority-public")
			}
		}
	}
	if !identifierPattern.MatchString(options.InstanceID) {
		return nil, fmt.Errorf("instance id %q is not a valid identifier", options.InstanceID)
	}
	if err := ensureProtectedRoot(d.Root); err != nil {
		return nil, err
	}
	if d.InstanceSource() {
		if err := os.MkdirAll(d.PolicyRoot, 0o755); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(d.abs(d.bootstrapRel(""))); err == nil {
		return nil, fmt.Errorf("the security directory is already initialized")
	}
	if entries, err := os.ReadDir(d.generationsAbs(GenerationsDir)); err == nil && len(entries) > 0 && !adopt {
		return nil, fmt.Errorf("the security directory is already initialized")
	}

	messagePEM, _, messageKeyID, err := NewKeyPEM()
	if err != nil {
		return nil, err
	}
	if err := d.writeFile(MessageKeyFile, messagePEM, 0o400); err != nil {
		return nil, err
	}

	var authorityID, authorityPublic string
	if options.Strict {
		public, err := decodeKey(options.AuthorityPublic)
		if err != nil {
			return nil, fmt.Errorf("strict mode needs --authority-public: %w", err)
		}
		if authorityID, err = trust.SigningKeyID(public); err != nil {
			return nil, err
		}
		authorityPublic = options.AuthorityPublic
	} else {
		authorityPEM, public, id, err := NewKeyPEM()
		if err != nil {
			return nil, err
		}
		if err := d.writeFile(AuthorityKeyFile, authorityPEM, 0o400); err != nil {
			return nil, err
		}
		authorityID, authorityPublic = id, rawBase64.EncodeToString(public)
	}

	document := bootstrap.Document{
		Format: bootstrap.Format, FormatVersion: bootstrap.FormatVersion,
		InstanceID: options.InstanceID, ActiveMessageSigner: "instance",
		MessageSigners: []bootstrap.MessageSigner{{
			Name: "instance", Purpose: "message-signing", Provider: "file", ExpectedKeyID: messageKeyID,
			Config: bootstrap.MessageSignerConfig{KeyFile: MessageKeyFile},
		}},
		ExternalPeers: []securityruntime.Peer{},
		PolicyAuthorities: []bootstrap.PolicyAuthority{{
			KeyID: authorityID, Algorithm: trust.SigningAlgorithm, PublicKey: authorityPublic,
		}},
		PolicySignatureThreshold: "1",
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	if !adopt {
		src := &policysrc.Source{
			Format: policysrc.Format, Version: policysrc.Version,
			InstanceID: options.InstanceID, PolicyID: PolicyID, Principals: map[string]*policysrc.Principal{},
		}
		if err := d.SaveSource(src); err != nil {
			return nil, err
		}
	}
	// The first generation is signed in solo mode; in strict mode the operator
	// signs it offline and installs it with `apply --signed`.
	result := &InitResult{MessageKeyID: messageKeyID, AuthorityKeyID: authorityID, Strict: options.Strict}
	if err := d.writeFile(d.bootstrapRel(""), append(encoded, '\n'), 0o600); err != nil {
		return nil, err
	}
	if options.Strict {
		return result, nil
	}
	applied, err := d.Apply(ApplyOptions{})
	if err != nil {
		return nil, err
	}
	result.Generation = applied.Generation
	return result, nil
}
