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

package trust

import "encoding/json"

const (
	PolicyFormat        = "brique-trust-policy"
	PolicyFormatVersion = "1"
	// PolicyFormatVersionV2 authorizes engine capacities by verb instead of by
	// name (SECURITY.md §2, D26).
	PolicyFormatVersionV2 = "2"
	// PolicyFormatVersionV3 is format 2 plus as_owner on a capacity/execute
	// permission (SECURITY.md §3.5, D24). A policy that does
	// not use as_owner stays format 2, so older engines keep reading it.
	PolicyFormatVersionV3 = "3"
	SigningAlgorithm      = "ed25519"

	MaxPolicyBytes   = 1 << 20
	MaxEnvelopeBytes = MaxPolicyBytes + 16<<10
)

type Envelope struct {
	Policy json.RawMessage `json:"policy"`
	Proofs []Proof         `json:"proofs"`
}

type Proof struct {
	AuthorityKeyID string `json:"authority_key_id"`
	Algorithm      string `json:"algorithm"`
	Signature      string `json:"signature"`
}

type Policy struct {
	Format             string      `json:"format"`
	FormatVersion      string      `json:"format_version"`
	PolicyID           string      `json:"policy_id"`
	InstanceID         string      `json:"instance_id"`
	Revision           string      `json:"revision"`
	PreviousPolicyHash string      `json:"previous_policy_hash"`
	IssuedAt           string      `json:"issued_at"`
	Principals         []Principal `json:"principals"`
	Scopes             []Scope     `json:"scopes"`
	// ExternalPeers (format version 2 and above) says where the instance reaches each
	// principal it sends to. It is part of what the authority signs: an address changes
	// only with the policy, never alone.
	ExternalPeers []ExternalPeer `json:"external_peers,omitempty"`
}

// ExternalPeer binds a principal to the remote instance it answers for: the audience
// its messages are addressed to, the local HTTPS adapter that carries them and the exact
// base URL of the remote listener.
//
// A peer reached on one machine has a Target. A peer reached on several has Endpoints instead:
// each is a machine, with its own base URL and the keys of the principal that live there.
type ExternalPeer struct {
	PrincipalID        string         `json:"principal_id"`
	AudienceInstanceID string         `json:"audience_instance_id"`
	Interface          string         `json:"interface"`
	Target             string         `json:"target,omitempty"`
	Endpoints          []PeerEndpoint `json:"endpoints,omitempty"`
}

// PeerEndpoint is one machine of a peer: its name, the base URL of its listener, and the
// identifiers of the principal's signing keys that live on it. A message is sealed to one of
// these keys, and the answer to a message signed by one of them goes to this machine.
type PeerEndpoint struct {
	Name   string   `json:"name"`
	Target string   `json:"target"`
	KeyIDs []string `json:"key_ids"`
}

// MaxPeerEndpoints bounds the machines one peer is reached on.
const MaxPeerEndpoints = 8

type Principal struct {
	PrincipalID string       `json:"principal_id"`
	Kind        string       `json:"kind"`
	Status      string       `json:"status"`
	ScopeIDs    []string     `json:"scope_ids"`
	SigningKeys []SigningKey `json:"signing_keys"`
	// Actors restricts the principal (format version 2 only): when present, the
	// access is the INTERSECTION of the principal's scopes and the scopes of the
	// actor named in the signed request. An absent actor uses the "default"
	// entry; an absent entry or an unknown actor is refused.
	Actors []ActorBinding `json:"actors,omitempty"`
	// ContentMode is the weakest way this principal's messages may reach the engine: absent or
	// "clear" accepts any, "hpke" accepts only content sealed to this instance's key. Responses
	// to the principal are sealed whenever its request was.
	ContentMode string `json:"content_mode,omitempty"`
	// Limits bounds how fast the principal can make this instance work. Absent: the defaults
	// below apply, so that no caller is unbounded unless the policy says so.
	Limits *Limits `json:"limits,omitempty"`
}

// Limits is a sustained rate of requests and the burst allowed above it, or an explicit
// exemption.
type Limits struct {
	// The numbers are strings in the signed policy, which has no JSON numbers.
	RatePerSecond int `json:"rate_per_second,omitempty,string"`
	Burst         int `json:"burst,omitempty,string"`
	// ChainCalls and ChainDepth bound what one admitted request may set off inside this
	// instance: the calls derived from it in all, and how deep they nest. Zero: the default.
	ChainCalls int  `json:"chain_calls,omitempty,string"`
	ChainDepth int  `json:"chain_depth,omitempty,string"`
	Unlimited  bool `json:"unlimited,omitempty"`
}

// Limits applied to a principal whose policy sets none.
const (
	DefaultNetworkRatePerSecond = 20
	DefaultNetworkBurst         = 40
	DefaultLocalRatePerSecond   = 100
	DefaultLocalBurst           = 200
	DefaultChainCalls           = 256
	DefaultChainDepth           = 16
	MaxChainCalls               = 100000
	MaxChainDepth               = 1000
	MaxRatePerSecond            = 100000
	MaxBurst                    = 1000000
)

// Content modes a principal can require. They are the wire content modes.
const (
	ContentModeClear = "clear"
	ContentModeHPKE  = "hpke"
)

// A local principal is an internal component the engine itself launched (a
// wrapper) and authenticates by its channel, not by a key (model §8.2). Its id is
// always LocalPrincipalPrefix followed by the wrapper name.
const (
	PrincipalKindLocal   = "local"
	LocalPrincipalPrefix = "local."
)

// DefaultActor is the entry that covers requests that carry no actor.
const DefaultActor = "default"

// ActorBinding gives one actor its own scopes.
type ActorBinding struct {
	ActorID  string   `json:"actor_id"`
	ScopeIDs []string `json:"scope_ids"`
}

type SigningKey struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Status    string `json:"status"`
	NotBefore string `json:"not_before,omitempty"`
	NotAfter  string `json:"not_after,omitempty"`
}

type Scope struct {
	ScopeID     string        `json:"scope_id"`
	Description string        `json:"description"`
	Allow       PermissionSet `json:"allow"`
	Deny        PermissionSet `json:"deny"`
}

type PermissionSet struct {
	Capabilities []CapabilityPermission `json:"capabilities"`
	Resources    []ResourcePermission   `json:"resources"`
	// Verbs is only valid in format version 2.
	Verbs []VerbPermission `json:"verbs,omitempty"`
}

// Verbs a v2 policy can grant on a context. "call" is not here: user capacities
// are authorized as capacity/execute resources.
const (
	VerbRead    = "read"
	VerbWrite   = "write"
	VerbCreate  = "create"
	VerbDefine  = "define"
	VerbControl = "control"
)

// VerbPermission grants (allow) or removes (deny) verbs on a context.
type VerbPermission struct {
	PermissionID string          `json:"permission_id"`
	Context      ContextSelector `json:"context"`
	Verbs        []string        `json:"verbs"`
}

type ContextSelector struct {
	Path               string `json:"path"`
	IncludeDescendants *bool  `json:"include_descendants"`
}

type CapabilityPermission struct {
	PermissionID string          `json:"permission_id"`
	Context      ContextSelector `json:"context"`
	Family       string          `json:"family"`
	Names        []string        `json:"names"`
}

type ResourcePermission struct {
	PermissionID string            `json:"permission_id"`
	Kind         string            `json:"kind"`
	Context      ContextSelector   `json:"context"`
	Selector     *ResourceSelector `json:"selector,omitempty"`
	Actions      []string          `json:"actions"`
	// AsOwner names a local principal whose rights, bounded to the subtree of the
	// capacity's context, replace the caller's for the capacity's own sub-calls.
	// Only on an allow permission of kind capacity with the execute action.
	AsOwner string `json:"as_owner,omitempty"`
}

type ResourceSelector struct {
	IDs []string `json:"ids,omitempty"`
	All *bool    `json:"all,omitempty"`
}

type Authority struct {
	KeyID     string
	PublicKey []byte
}

type DecodeResult struct {
	envelope        Envelope
	policy          Policy
	canonicalPolicy []byte
	policyHash      string
}

func (result *DecodeResult) PolicyID() string   { return result.policy.PolicyID }
func (result *DecodeResult) InstanceID() string { return result.policy.InstanceID }
func (result *DecodeResult) Revision() string   { return result.policy.Revision }
func (result *DecodeResult) PolicyHash() string { return result.policyHash }

// ExternalPeers returns a copy of the peers the signed policy carries.
func (result *DecodeResult) ExternalPeers() []ExternalPeer {
	return append([]ExternalPeer(nil), result.policy.ExternalPeers...)
}

// PreviousPolicyHash is the hash this policy declares as its predecessor.
func (result *DecodeResult) PreviousPolicyHash() string { return result.policy.PreviousPolicyHash }

func (result *DecodeResult) CanonicalBytes() []byte {
	return append([]byte(nil), result.canonicalPolicy...)
}
