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

// Package admission owns the immutable authority sidecar created after an
// external intention has been cryptographically verified and strictly decoded.
package admission

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/security/inner"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
)

var ErrInvalidAdmission = errors.New("invalid external admission")

// Context is immutable after construction. It contains no private key, raw
// signature, application payload, or mutable scope map. The retained trust
// admission pins the exact immutable snapshot that admitted this root call.
type Context struct {
	trustAdmission  trust.Admission
	principalID     string
	principalKind   string
	signingKeyID    string
	authenticatedAt time.Time
	requestHash     string
	rootIntentionID string
	contentMode     string
	sourceNamespace string
	awaitResponse   bool
	policyRevision  string
	policyHash      string
	// local marks the admission of a restricted internal component: no key, no
	// signature, no signed envelope, the channel was the proof.
	local bool
	// public and localUI mark browser subjects. Their authority comes from
	// policy, while channelID is operational ownership created by the ingress;
	// none is supplied by the page.
	public    bool
	localUI   bool
	channelID string
	// owner is the local principal whose rights this admission carries instead of
	// the caller's, when the capacity runs as_owner. The caller's identity,
	// correlation and response state are unchanged.
	owner string
}

var publicCoordinatePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// NewPublicWeb binds an anonymous browser intention to the reserved public.web
// policy subject and a server-created channel. It replaces the complete source
// address so page-controlled bytes can never claim an UI, actor or owner.
func NewPublicWeb(trustAdmission trust.Admission, channelID, uiName string, message circulation.Message, now time.Time) (circulation.Message, *Context, error) {
	if message.Kind != circulation.ValueKindIntention || message.Intention.IntentionID == "" || now.IsZero() ||
		trustAdmission.PrincipalKind() != trust.PrincipalKindPublic || trustAdmission.PrincipalID() != trust.PublicWebPrincipalID ||
		!publicCoordinatePattern.MatchString(channelID) || !publicCoordinatePattern.MatchString(uiName) {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	message.Intention.From = circulation.Address{
		Context: circulation.ContextID("@public_web:/" + channelID + "/" + uiName),
		Type:    "public",
		Cap:     "web",
	}
	message.Intention.Identity = circulation.Identity{}
	context := &Context{
		trustAdmission:  trustAdmission,
		principalID:     trust.PublicWebPrincipalID,
		principalKind:   trust.PrincipalKindPublic,
		authenticatedAt: now.UTC(),
		rootIntentionID: message.Intention.IntentionID,
		contentMode:     "public",
		sourceNamespace: channelID + "/" + uiName,
		awaitResponse:   message.Intention.AwaitResponse,
		policyRevision:  trustAdmission.PolicyRevision(),
		policyHash:      trustAdmission.PolicyHash(),
		public:          true,
		channelID:       channelID,
	}
	if !context.Valid() {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	return message, context, nil
}

// NewLocalUI binds a request from an authenticated browser bridge to the
// explicitly configured local.ui authority. It manufactures the source and
// clears page-provided identity exactly like public admission, but carries the
// narrower local UI policy rather than public.web.
func NewLocalUI(trustAdmission trust.Admission, channelID, uiName string, message circulation.Message, now time.Time) (circulation.Message, *Context, error) {
	if message.Kind != circulation.ValueKindIntention || message.Intention.IntentionID == "" || now.IsZero() ||
		trustAdmission.PrincipalKind() != trust.PrincipalKindLocal || trustAdmission.PrincipalID() != trust.LocalUIPrincipalID ||
		!publicCoordinatePattern.MatchString(channelID) || !publicCoordinatePattern.MatchString(uiName) {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	message.Intention.From = circulation.Address{
		Context: circulation.ContextID("@local_ui:/" + channelID + "/" + uiName),
		Type:    "local_ui",
		Cap:     "web",
	}
	message.Intention.Identity = circulation.Identity{}
	context := &Context{
		trustAdmission:  trustAdmission,
		principalID:     trust.LocalUIPrincipalID,
		principalKind:   trust.PrincipalKindLocal,
		authenticatedAt: now.UTC(),
		rootIntentionID: message.Intention.IntentionID,
		contentMode:     "local_ui",
		sourceNamespace: channelID + "/" + uiName,
		awaitResponse:   message.Intention.AwaitResponse,
		policyRevision:  trustAdmission.PolicyRevision(),
		policyHash:      trustAdmission.PolicyHash(),
		localUI:         true,
		channelID:       channelID,
	}
	if !context.Valid() {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	return message, context, nil
}

// DecodeIntention is the sole phase-1 constructor. Decoding and admission are
// one operation so a caller cannot attach a verified envelope to different
// application bytes.
func DecodeIntention(verified *wire.Verified) (circulation.Message, *Context, error) {
	if verified == nil || verified.Protected().Purpose != wire.PurposeIntention || verified.VerifiedAt().IsZero() {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	message, err := inner.DecodeIntention(verified)
	if err != nil {
		return circulation.Message{}, nil, fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	trustAdmission := verified.Admission()
	principalID := trustAdmission.PrincipalID()
	prefix := "@ext_" + principalID + ":"
	projectedSource := string(message.Intention.From.Context)
	if principalID == "" || !strings.HasPrefix(projectedSource, prefix) || len(projectedSource) == len(prefix) {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	protected := verified.Protected()
	context := &Context{
		trustAdmission:  trustAdmission,
		principalID:     principalID,
		principalKind:   trustAdmission.PrincipalKind(),
		signingKeyID:    trustAdmission.SigningKeyID(),
		authenticatedAt: verified.VerifiedAt().UTC(),
		requestHash:     verified.SignedEnvelopeHash(),
		rootIntentionID: protected.IntentionID,
		contentMode:     protected.ContentMode,
		sourceNamespace: strings.TrimPrefix(projectedSource, prefix),
		awaitResponse:   message.Intention.AwaitResponse,
		policyRevision:  trustAdmission.PolicyRevision(),
		policyHash:      trustAdmission.PolicyHash(),
	}
	if !context.Valid() {
		return circulation.Message{}, nil, ErrInvalidAdmission
	}
	return message, context, nil
}

// NewRestrictedLocal admits an intention emitted by an internal component that the
// policy restricts (a wrapper with a local.<name> principal). The caller has
// already proven the channel (the per-launch secret); there is no signature, so
// the admission is a distinct kind of Context and is not a signed one dressed up.
func NewRestrictedLocal(trustAdmission trust.Admission, wrapperName string, message circulation.Message, now time.Time) (*Context, error) {
	if message.Kind != circulation.ValueKindIntention || message.Intention.IntentionID == "" ||
		trustAdmission.PrincipalKind() != trust.PrincipalKindLocal || trustAdmission.PrincipalID() != trust.LocalPrincipalPrefix+wrapperName || now.IsZero() {
		return nil, ErrInvalidAdmission
	}
	context := &Context{
		trustAdmission:  trustAdmission,
		principalID:     trustAdmission.PrincipalID(),
		principalKind:   trust.PrincipalKindLocal,
		authenticatedAt: now.UTC(),
		rootIntentionID: message.Intention.IntentionID,
		contentMode:     "local",
		sourceNamespace: wrapperName,
		awaitResponse:   message.Intention.AwaitResponse,
		policyRevision:  trustAdmission.PolicyRevision(),
		policyHash:      trustAdmission.PolicyHash(),
		local:           true,
	}
	if !context.Valid() {
		return nil, ErrInvalidAdmission
	}
	return context, nil
}

// DeriveOwner returns the admission under which a user capacity runs when the rule
// that lets this caller call it names an owner (D24): same caller, same root
// request and response state, but the authority is the owner's, bounded to the
// capacity's context and checked against the current policy at every use. It
// returns the receiver itself when no owner applies. A non-nil error means the
// policy names an owner that can no longer be admitted: the call must be refused.
func (context *Context) DeriveOwner(runtime *trust.Runtime, contextPath, capacity string) (*Context, error) {
	if !context.Valid() || runtime == nil {
		return nil, ErrInvalidAdmission
	}
	admitted, owned, err := runtime.AdmitOwner(context.trustAdmission, contextPath, capacity)
	if err != nil {
		return nil, err
	}
	if !owned {
		return context, nil
	}
	derived := *context
	derived.trustAdmission = admitted
	derived.owner = admitted.PrincipalID()
	return &derived, nil
}

// OwnerID is the owner this admission acts as, or "".
func (context *Context) OwnerID() string {
	if context == nil {
		return ""
	}
	return context.owner
}

// IsRestrictedLocal reports whether this is the admission of a restricted
// internal component rather than of a signed external caller.
func (context *Context) IsRestrictedLocal() bool { return context != nil && context.local }

// IsPublicWeb reports whether the dedicated public ingress admitted the call.
func (context *Context) IsPublicWeb() bool { return context != nil && context.public }

// IsLocalUI reports whether an authenticated browser bridge to this same
// instance created the admission.
func (context *Context) IsLocalUI() bool { return context != nil && context.localUI }

// ChannelID is the engine-created operational owner of a public or local UI
// call. It is empty for signed and restricted-wrapper admissions.
func (context *Context) ChannelID() string {
	if context == nil || (!context.public && !context.localUI) {
		return ""
	}
	return context.channelID
}

func (context *Context) Valid() bool {
	if context != nil && context.public {
		return context.principalID == trust.PublicWebPrincipalID && context.principalKind == trust.PrincipalKindPublic &&
			context.signingKeyID == "" && !context.local && !context.authenticatedAt.IsZero() &&
			context.rootIntentionID != "" && context.contentMode == "public" && context.sourceNamespace != "" &&
			publicCoordinatePattern.MatchString(context.channelID) && context.policyRevision != "" && context.policyHash != ""
	}
	if context != nil && context.localUI {
		return context.principalID == trust.LocalUIPrincipalID && context.principalKind == trust.PrincipalKindLocal &&
			context.signingKeyID == "" && !context.local && !context.public && !context.authenticatedAt.IsZero() &&
			context.rootIntentionID != "" && context.contentMode == "local_ui" && context.sourceNamespace != "" &&
			publicCoordinatePattern.MatchString(context.channelID) && context.policyRevision != "" && context.policyHash != ""
	}
	if context != nil && context.local {
		return context.principalID != "" && context.principalKind == trust.PrincipalKindLocal && context.signingKeyID == "" &&
			!context.authenticatedAt.IsZero() && context.rootIntentionID != "" && context.sourceNamespace != "" &&
			context.policyRevision != "" && context.policyHash != ""
	}
	return context != nil && context.principalID != "" && context.principalKind != "" &&
		context.signingKeyID != "" && !context.authenticatedAt.IsZero() &&
		context.requestHash != "" && context.rootIntentionID != "" &&
		context.contentMode != "" && context.sourceNamespace != "" &&
		context.policyRevision != "" && context.policyHash != ""
}

func (context *Context) PrincipalID() string   { return context.principalID }
func (context *Context) PrincipalKind() string { return context.principalKind }

// ActorID is the actor the sender signed into the request, or "". It narrows the
// principal and is traced; it is a claim, never a proof.
func (context *Context) ActorID() string      { return context.trustAdmission.ActorID() }
func (context *Context) SigningKeyID() string { return context.signingKeyID }

// SigningPublicKey is the key that signed the request: a response sealed to the requester is
// sealed to it.
func (context *Context) SigningPublicKey() ed25519.PublicKey {
	return context.trustAdmission.PublicKey()
}
func (context *Context) AuthenticatedAt() time.Time { return context.authenticatedAt }
func (context *Context) RequestHash() string        { return context.requestHash }
func (context *Context) RootIntentionID() string    { return context.rootIntentionID }
func (context *Context) ContentMode() string        { return context.contentMode }
func (context *Context) SourceNamespace() string    { return context.sourceNamespace }
func (context *Context) AwaitResponse() bool        { return context.awaitResponse }
func (context *Context) PolicyRevision() string     { return context.policyRevision }
func (context *Context) PolicyHash() string         { return context.policyHash }

// AuthorizeCapability checks the intersection of the admitting snapshot and
// the runtime's currently active snapshot.
//
// Engine capacities are opened by the verbs of their class (D26); a capacity
// with no known class has no verb and is refused under a v2 snapshot.
func (context *Context) AuthorizeCapability(runtime *trust.Runtime, contextPath, family, name string) bool {
	if !context.Valid() || runtime == nil {
		return false
	}
	var verbs []string
	if class, known := access.ClassOf(family, name); known {
		verbs = class.Verbs()
	}
	return runtime.AuthorizeCapabilityVerbs(context.trustAdmission, contextPath, family, name, verbs)
}

// AuthorizeVerb checks that the principal holds a verb on one context, against the
// admitting and the current snapshots.
func (context *Context) AuthorizeVerb(runtime *trust.Runtime, contextPath, verb string) bool {
	return context.Valid() && runtime != nil && runtime.AuthorizeVerb(context.trustAdmission, contextPath, verb)
}

// AuthorizeResource applies the same two-snapshot intersection to one fully
// normalized resource access.
func (context *Context) AuthorizeResource(runtime *trust.Runtime, contextPath, kind, id, action string) bool {
	return context.Valid() && runtime != nil && runtime.AuthorizeResource(context.trustAdmission, contextPath, kind, id, action)
}

// AuthorizeResources applies all-or-nothing dual-snapshot authorization to a
// complete extracted access set. Invalid or non-canonical accesses fail closed.
func (context *Context) AuthorizeResources(runtime *trust.Runtime, accesses []access.ResourceAccess) bool {
	if !context.Valid() || runtime == nil {
		return false
	}
	for _, item := range accesses {
		normalized, err := access.Normalize(item)
		if err != nil || !runtime.AuthorizeResource(
			context.trustAdmission,
			string(normalized.Context),
			normalized.Kind,
			normalized.ID,
			normalized.Action,
		) {
			return false
		}
	}
	return true
}

func (Context) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("external admission is internal and cannot be serialized")
}

func (*Context) UnmarshalJSON([]byte) error {
	return fmt.Errorf("external admission is internal and cannot be deserialized")
}

var _ json.Marshaler = (*Context)(nil)
var _ json.Unmarshaler = (*Context)(nil)
