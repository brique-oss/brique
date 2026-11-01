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

// Package audit is the denial journal of the instance (SECURITY.md
// §11). It records why an external access was refused, never what the message
// contained. Recording is non-blocking and bounded: the hot path never waits for
// the disk, and an overflow is counted, not lost silently.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Closed set of reason codes (model §11.3).
const (
	ReasonUnknownKey         = "unknown_key"
	ReasonKeyInactive        = "key_inactive"
	ReasonPrincipalInactive  = "principal_inactive"
	ReasonExpired            = "expired"
	ReasonReplay             = "replay"
	ReasonAudienceMismatch   = "audience_mismatch"
	ReasonBadSignature       = "bad_signature"
	ReasonCapabilityDenied   = "capability_denied"
	ReasonResourceDenied     = "resource_denied"
	ReasonActorDenied        = "actor_denied"
	ReasonEgressDenied       = "egress_denied"
	ReasonWrapperSecret      = "wrapper_secret_invalid"
	ReasonPayloadLimit       = "payload_limit"
	ReasonUnsupported        = "unsupported"
	ReasonQuota              = "quota_exceeded"
	ReasonInternal           = "internal"
	reasonSuppressedInternal = "suppressed"
)

// Layers at which a refusal happens.
const (
	LayerAuthentication = "authentication"
	LayerCapability     = "capability"
	LayerResource       = "resource"
	LayerReplay         = "replay"
	LayerTransport      = "transport"
)

var reasons = map[string]bool{
	ReasonUnknownKey: true, ReasonKeyInactive: true, ReasonPrincipalInactive: true, ReasonExpired: true,
	ReasonReplay: true, ReasonAudienceMismatch: true, ReasonBadSignature: true, ReasonCapabilityDenied: true,
	ReasonResourceDenied: true, ReasonActorDenied: true, ReasonEgressDenied: true, ReasonWrapperSecret: true,
	ReasonPayloadLimit: true, ReasonUnsupported: true, ReasonQuota: true, ReasonInternal: true,
}

// ValidReason reports whether code belongs to the closed set.
func ValidReason(code string) bool { return reasons[code] }

// Resource is the normalized resource of a resource-layer refusal.
type Resource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Action string `json:"action"`
}

// Record is one line of the journal. Fields supplied by an unauthenticated
// caller (key id, context, capacity, intention ids) are marked by Untrusted and
// must be read as claims, never as facts.
type Record struct {
	TS             string    `json:"ts,omitempty"`
	PolicyRevision string    `json:"policy_revision,omitempty"`
	PolicyHash     string    `json:"policy_hash,omitempty"`
	Principal      string    `json:"principal,omitempty"`
	Actor          string    `json:"actor,omitempty"`
	KeyID          string    `json:"key_id,omitempty"`
	Channel        string    `json:"channel,omitempty"`
	RootIntention  string    `json:"root_intention,omitempty"`
	Intention      string    `json:"intention,omitempty"`
	Context        string    `json:"context,omitempty"`
	Capacity       string    `json:"capacity,omitempty"`
	Layer          string    `json:"layer,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Resource       *Resource `json:"resource,omitempty"`
	PermissionID   string    `json:"permission_id,omitempty"`
	AccessDigest   string    `json:"access_digest,omitempty"`
	Count          int       `json:"count,omitempty"`
	Untrusted      bool      `json:"untrusted,omitempty"`
	// Suppressed is set on the overflow line: events counted but not recorded.
	Suppressed int `json:"suppressed,omitempty"`
}

// Recorder receives refusals. Implementations must not block.
type Recorder interface {
	Record(Record)
}

type nop struct{}

func (nop) Record(Record) {}

// Nop is the recorder used when no journal is configured.
var Nop Recorder = nop{}

// MaxRecordBytes bounds one line of the journal.
const MaxRecordBytes = 2048

// Field bounds: a record with every field at its bound still fits MaxRecordBytes.
const (
	maxField   = 80
	maxContext = 200
)

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return value[:cut]
}

// normalize bounds every field and forces the reason into the closed set.
func (r Record) normalize() Record {
	if !reasons[r.Reason] && r.Suppressed == 0 {
		r.Reason = ReasonInternal
	}
	r.PolicyRevision = clip(r.PolicyRevision, 20)
	r.PolicyHash = clip(r.PolicyHash, 60)
	r.Principal = clip(r.Principal, maxField)
	r.Actor = clip(r.Actor, maxField)
	r.KeyID = clip(r.KeyID, 60)
	r.Channel = clip(r.Channel, 32)
	r.RootIntention = clip(r.RootIntention, 40)
	r.Intention = clip(r.Intention, 40)
	r.Context = clip(r.Context, maxContext)
	r.Capacity = clip(r.Capacity, maxField)
	r.Layer = clip(r.Layer, 32)
	r.PermissionID = clip(r.PermissionID, maxField)
	if r.Resource != nil {
		resource := *r.Resource
		resource.Kind, resource.ID, resource.Action = clip(resource.Kind, 32), clip(resource.ID, maxField), clip(resource.Action, 32)
		r.Resource = &resource
	}
	if r.AccessDigest == "" {
		r.AccessDigest = digest(r)
	}
	return r
}

// digest is a stable identity of the refused access, without its content.
func digest(r Record) string {
	h := sha256.New()
	for _, part := range []string{r.Principal, r.Actor, r.Context, r.Capacity, r.Layer, r.Reason} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	if r.Resource != nil {
		h.Write([]byte(r.Resource.Kind + "\x00" + r.Resource.ID + "\x00" + r.Resource.Action))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:32]
}

// aggregationKey says which refusals are "the same" inside the window (model
// §11.4): same principal, reason and resource.
func (r Record) aggregationKey() string {
	var b strings.Builder
	for _, part := range []string{r.Principal, r.Actor, r.KeyID, r.Reason, r.Layer, r.Context, r.Capacity, r.Channel} {
		b.WriteString(part)
		b.WriteByte(0)
	}
	if r.Resource != nil {
		b.WriteString(r.Resource.Kind + "\x00" + r.Resource.ID + "\x00" + r.Resource.Action)
	}
	if r.Untrusted {
		b.WriteString("\x00u")
	}
	return b.String()
}

// encode marshals a normalized record within MaxRecordBytes, degrading the
// optional fields before ever exceeding the bound.
func encode(r Record) ([]byte, error) {
	line, err := json.Marshal(r)
	if err == nil && len(line) <= MaxRecordBytes {
		return line, nil
	}
	r.Context, r.Capacity, r.RootIntention, r.PolicyHash = clip(r.Context, 64), clip(r.Capacity, 32), "", ""
	if r.Resource != nil {
		resource := Resource{Kind: r.Resource.Kind, Action: r.Resource.Action, ID: clip(r.Resource.ID, 24)}
		r.Resource = &resource
	}
	line, err = json.Marshal(r)
	if err != nil || len(line) > MaxRecordBytes {
		r.Context, r.Capacity, r.Intention, r.KeyID, r.PermissionID = "", "", "", "", ""
		return json.Marshal(r)
	}
	return line, nil
}
