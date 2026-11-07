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

package policysrc

// source.go
//
// The policy source: a short, human-written document (model §5) that the
// compiler turns into the signed trust-policy format. Decoding is strict (no
// duplicate keys, no unknown field, no number, no trailing data).

import (
	"bytes"
	"encoding/json"
	"fmt"

	"brique_engine/security/trust"
)

const (
	// Format and Version identify the source document.
	Format  = "brique-access-source"
	Version = "1"

	VerbRead    = "read"
	VerbWrite   = "write"
	VerbCreate  = "create"
	VerbCall    = "call"
	VerbDefine  = "define"
	VerbControl = "control"
)

// Source is the whole document.
type Source struct {
	Format     string                `json:"format"`
	Version    string                `json:"version"`
	InstanceID string                `json:"instance_id"`
	PolicyID   string                `json:"policy_id"`
	Principals map[string]*Principal `json:"principals"`
	// Peers says where the instance reaches the principals it sends to. They compile
	// into the signed policy: an address changes only with the policy.
	Peers []trust.ExternalPeer `json:"peers,omitempty"`
}

// Values of Principal.Encryption.
const (
	EncryptionRequired = "required"
	EncryptionNone     = "none"
)

// Sealed reports whether messages to and from the principal must be encrypted.
func (p *Principal) Sealed() bool {
	switch p.Encryption {
	case EncryptionRequired:
		return p.Kind != "local"
	case EncryptionNone:
		return false
	}
	return p.Kind == "instance"
}

// Limits is the request rate of a principal: a rate per second and the burst allowed above it,
// or "unlimited". The numbers are written as strings, like every number of a source.
type Limits struct {
	RatePerSecond int  `json:"rate_per_second,omitempty,string"`
	Burst         int  `json:"burst,omitempty,string"`
	ChainCalls    int  `json:"chain_calls,omitempty,string"`
	ChainDepth    int  `json:"chain_depth,omitempty,string"`
	Unlimited     bool `json:"unlimited,omitempty"`
}

// Principal is one subject of authorization.
type Principal struct {
	Kind          string `json:"kind"`
	Disabled      bool   `json:"disabled,omitempty"`
	Keys          []Key  `json:"keys,omitempty"`
	TracePayloads bool   `json:"trace_payloads,omitempty"`
	// Encryption is how messages to and from this principal travel. An instance principal
	// is sealed by default; "none" opts it out. A service or user principal is in the clear
	// unless "required". Messages are sealed to the principal's key and opened with this
	// instance's own.
	Encryption string `json:"encryption,omitempty"`
	// Limits bounds the requests per second the principal can make. Absent, a default
	// applies; "unlimited" must be written to lift it.
	Limits *Limits           `json:"limits,omitempty"`
	Grants []Grant           `json:"grants,omitempty"`
	Actors map[string]*Actor `json:"actors,omitempty"`
}

// Key is a signing public key of a principal.
type Key struct {
	PublicKey string `json:"public_key"`
	State     string `json:"state,omitempty"`
	// NotBefore is when the key starts to be valid (RFC 3339, UTC). Of the keys of a principal that
	// live on one machine, the one that starts last is the one a message is sealed to.
	NotBefore string `json:"not_before,omitempty"`
}

// Actor narrows a principal for one actor label (phase 6).
type Actor struct {
	Grants []Grant `json:"grants"`
}

// Grant is one verb over one or several targets. Exactly one verb is set.
type Grant struct {
	Read    TargetList `json:"read,omitempty"`
	Write   TargetList `json:"write,omitempty"`
	Create  TargetList `json:"create,omitempty"`
	Call    TargetList `json:"call,omitempty"`
	Define  TargetList `json:"define,omitempty"`
	Control TargetList `json:"control,omitempty"`
	Except  []string   `json:"except,omitempty"`
	AsOwner string     `json:"as_owner,omitempty"`
}

// TargetList accepts a single string or an array of strings.
type TargetList []string

// UnmarshalJSON implements json.Unmarshaler.
func (t *TargetList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("empty target")
	}
	switch data[0] {
	case '"':
		var one string
		if err := json.Unmarshal(data, &one); err != nil {
			return err
		}
		*t = TargetList{one}
	case '[':
		var many []string
		if err := json.Unmarshal(data, &many); err != nil {
			return err
		}
		if many == nil {
			many = []string{}
		}
		*t = TargetList(many)
	default:
		return fmt.Errorf("a target must be a string or an array of strings")
	}
	return nil
}

// verbAndTargets returns the grant's verb and targets, and whether exactly one
// verb is present.
func (g Grant) verbAndTargets() (verb string, targets TargetList, ok bool) {
	count := 0
	for _, candidate := range []struct {
		verb    string
		targets TargetList
	}{{VerbRead, g.Read}, {VerbWrite, g.Write}, {VerbCreate, g.Create}, {VerbCall, g.Call}, {VerbDefine, g.Define}, {VerbControl, g.Control}} {
		if candidate.targets != nil {
			count++
			verb, targets = candidate.verb, candidate.targets
		}
	}
	return verb, targets, count == 1
}

// Decode parses and strictly validates the JSON shape of a source. It does not
// validate its meaning: call Validate.
func Decode(data []byte) (*Source, error) {
	var src Source
	if err := trust.StrictDecodeJSON(data, &src); err != nil {
		return nil, fmt.Errorf("access source: %w", err)
	}
	return &src, nil
}

// Verb returns the grant's single verb and its targets; ok is false when the
// grant does not have exactly one verb.
func (g Grant) Verb() (verb string, targets []string, ok bool) {
	verb, list, ok := g.verbAndTargets()
	return verb, []string(list), ok
}
