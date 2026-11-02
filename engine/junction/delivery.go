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

package junction

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"brique_engine/circulation"
	"brique_engine/security/admission"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
)

// LocalAuthority is an opaque authority issued by the runtime to a trusted
// local component. It is deliberately not part of circulation.Message and is
// therefore never serialized on a wire boundary.
type LocalAuthority struct {
	security *deliverySecurity
}

// NewLocalAuthority creates the phase-0 authority handle used while internal
// routes migrate from Message to Delivery. The source is diagnostic runtime
// identity, never an identity claimed by message content.
func NewLocalAuthority(source string) (LocalAuthority, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return LocalAuthority{}, fmt.Errorf("local authority source is required")
	}
	return LocalAuthority{security: &deliverySecurity{localSource: source}}, nil
}

// MustLocalAuthority is intended for fixed runtime component identities.
func MustLocalAuthority(source string) LocalAuthority {
	authority, err := NewLocalAuthority(source)
	if err != nil {
		panic(err)
	}
	return authority
}

// Source returns the runtime identity that owns this local authority.
func (a LocalAuthority) Source() string {
	if a.security == nil {
		return ""
	}
	return a.security.localSource
}

// Delivery is the transport-neutral internal envelope. Its security fields
// are private so they cannot be supplied through decoded JSON. The zero value
// is invalid and must be rejected by every protected internal route.
type Delivery struct {
	message  circulation.Message
	security *deliverySecurity
}

type deliverySecurity struct {
	localSource string
	external    *admission.Context
	responses   *responseState
	response    *ResponseReservation
	wrapper     string
	wrapperBack *wrapperReturnRoute
	// restricted marks a delivery admitted from a restricted internal component:
	// it is authorized like an external one, but it is answered locally (no
	// signed response, no permit).
	restricted bool
}

// MarshalJSON prevents an internal authority envelope from crossing a wire
// boundary accidentally. Call Message explicitly at an authorized adapter.
func (Delivery) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("junction.Delivery is internal and cannot be serialized")
}

// UnmarshalJSON prevents decoded wire content from constructing a Delivery.
func (*Delivery) UnmarshalJSON([]byte) error {
	return fmt.Errorf("junction.Delivery is internal and cannot be deserialized")
}

// NewLocalDelivery attaches an explicit runtime authority to a local message.
func NewLocalDelivery(authority LocalAuthority, message circulation.Message) (Delivery, error) {
	if authority.security == nil {
		return Delivery{}, fmt.Errorf("invalid local authority")
	}
	return Delivery{
		message:  message,
		security: authority.security,
	}, nil
}

// AdmitExternalIntention is the sole external root-delivery constructor. It
// binds the exact verified payload to its immutable admission before either is
// exposed to internal routing.
func AdmitExternalIntention(verified *wire.Verified) (Delivery, error) {
	message, context, err := admission.DecodeIntention(verified)
	if err != nil {
		return Delivery{}, err
	}
	security := &deliverySecurity{external: context}
	if context.AwaitResponse() {
		security.responses = newResponseState(context)
	}
	return Delivery{message: message, security: security}, nil
}

// AdmitRestrictedLocal is the constructor of the delivery of an intention emitted
// by a restricted internal component. Everything downstream sees an admitted
// delivery and applies the same doors as for an external caller; only the answer
// differs (BindExternalResponse).
func AdmitRestrictedLocal(message circulation.Message, context *admission.Context) (Delivery, error) {
	if context == nil || !context.IsRestrictedLocal() || !context.Valid() ||
		message.Kind != circulation.ValueKindIntention || message.Intention.IntentionID != context.RootIntentionID() {
		return Delivery{}, fmt.Errorf("a valid restricted local admission is required")
	}
	return Delivery{message: message, security: &deliverySecurity{external: context, restricted: true}}, nil
}

// RestrictedLocal returns the admission of a restricted internal component, when
// that is what admitted this delivery.
func (d Delivery) RestrictedLocal() (*admission.Context, bool) {
	if !d.Valid() || d.security.external == nil || !d.security.restricted {
		return nil, false
	}
	return d.security.external, true
}

// ErrDerivedCapabilityDenied is returned when the admission a derived intention would run
// under does not hold the destination capability. Callers that have a waiting party answer
// it with the canonical capability refusal instead of leaving it to time out.
var ErrDerivedCapabilityDenied = errors.New("derived capability is not authorized")

// DeriveExternal creates a child intention under the exact admission carried
// by its parent. It cannot widen authority: correlation must bind the child to
// the parent/root chain and the destination capability must be allowed by both
// the admitting and currently active trust snapshots.
func DeriveExternal(parent Delivery, child circulation.Message, runtime *trust.Runtime) (Delivery, error) {
	return DeriveExternalAudited(parent, child, runtime, audit.Nop)
}

// DeriveExternalAudited is DeriveExternal that also journals a refused derived
// capability under the parent's admission.
func DeriveExternalAudited(parent Delivery, child circulation.Message, runtime *trust.Runtime, recorder audit.Recorder) (Delivery, error) {
	context, external := parent.ExternalAdmission()
	parentMessage, valid := parent.Message()
	if !valid || !external || runtime == nil || parentMessage.Kind != circulation.ValueKindIntention {
		return Delivery{}, fmt.Errorf("valid external parent delivery is required")
	}
	if child.Kind != circulation.ValueKindIntention || child.Intention.IntentionID == "" ||
		child.Intention.IntentionID == parentMessage.Intention.IntentionID {
		return Delivery{}, fmt.Errorf("derived intention requires a distinct non-empty id")
	}
	correlation := child.Intention.Correlation
	expectedCorrelationRoot := context.RootIntentionID()
	if parentCorrelation := parentMessage.Intention.Correlation; parentCorrelation != nil && parentCorrelation.RootIntentionID != "" {
		expectedCorrelationRoot = parentCorrelation.RootIntentionID
	}
	if correlation == nil || correlation.ParentIntentionID != parentMessage.Intention.IntentionID ||
		correlation.RootIntentionID != expectedCorrelationRoot {
		return Delivery{}, fmt.Errorf("derived intention correlation does not match its external chain")
	}
	identity := child.Intention.Identity
	if identity.ID != "" || identity.Kind != "" {
		return Delivery{}, fmt.Errorf("derived intention cannot replace external identity")
	}
	to := child.Intention.To
	if !context.AuthorizeCapability(runtime, string(to.Context), to.Type, to.Cap) {
		context.RecordCapabilityDenial(recorder, runtime, "derived", child.Intention)
		return Delivery{}, ErrDerivedCapabilityDenied
	}
	// Derived work keeps the admission and root response state, but never
	// inherits transport-only response or wrapper-dispatch metadata.
	return Delivery{message: child, security: &deliverySecurity{
		external: context, responses: parent.security.responses, restricted: parent.security.restricted,
	}}, nil
}

// DeriveAsOwner returns the delivery under which a user capacity runs when the
// rule that opened it for the caller names an owner (D24). The message, the root
// response state and the wrapper routes are those of the request; only the
// authority changes, to the owner's, bounded to the capacity's context. When no
// owner applies the request itself is returned. The original request must be
// kept for the signed root response: this delivery is for the capacity's
// own sub-calls.
func DeriveAsOwner(request Delivery, runtime *trust.Runtime) (Delivery, error) {
	context, external := request.ExternalAdmission()
	message, valid := request.Message()
	if !valid || !external || message.Kind != circulation.ValueKindIntention || message.Intention.To.Type != circulation.ValueTypeUser {
		return Delivery{}, fmt.Errorf("an admitted user request is required")
	}
	owned, err := context.DeriveOwner(runtime, string(message.Intention.To.Context), message.Intention.To.Cap)
	if err != nil {
		return Delivery{}, err
	}
	if owned == context {
		return request, nil
	}
	security := *request.security
	security.external = owned
	return Delivery{message: message, security: &security}, nil
}

// DispatchExternalToWrapper marks an admitted intention for one wrapper
// implementation boundary without rewriting its logical message. Comm uses
// this private route metadata to produce the legacy @wrapper transport address
// only in the physical wrapper frame.
func DispatchExternalToWrapper(request Delivery, wrapperName string) (Delivery, error) {
	message, valid := request.Message()
	context, external := request.ExternalAdmission()
	wrapperName = strings.TrimSpace(wrapperName)
	if !valid || !external || context == nil || message.Kind != circulation.ValueKindIntention ||
		message.Intention.IntentionID == "" || message.Intention.To.Type != circulation.ValueTypeUser ||
		!strings.HasPrefix(strings.TrimSpace(string(message.Intention.To.Context)), "/") ||
		!validWrapperName(wrapperName) {
		return Delivery{}, fmt.Errorf("valid external wrapper request is required")
	}
	if request.security.wrapper != "" {
		return Delivery{}, fmt.Errorf("external wrapper dispatch is already selected")
	}
	security := &deliverySecurity{
		external: context, responses: request.security.responses, wrapper: wrapperName, restricted: request.security.restricted,
	}
	if request.security.wrapperBack != nil {
		route := *request.security.wrapperBack
		security.wrapperBack = &route
	}
	return Delivery{message: message, security: security}, nil
}

// BeginExternalResponse converts a root request Delivery into a response-only
// Delivery. The destination is forced to the authenticated request origin and
// the attached reservation must be committed only after signed-envelope
// delivery succeeds, or aborted on local failure.
func BeginExternalResponse(request Delivery, response circulation.Message) (Delivery, ResponseReservation, error) {
	context, external := request.ExternalAdmission()
	requestMessage, valid := request.Message()
	if !valid || !external || requestMessage.Kind != circulation.ValueKindIntention ||
		requestMessage.Intention.IntentionID != context.RootIntentionID() {
		return Delivery{}, ResponseReservation{}, fmt.Errorf("external root request delivery is required")
	}
	if response.Kind != circulation.ValueKindResponse || response.Response.IntentionID != context.RootIntentionID() {
		return Delivery{}, ResponseReservation{}, fmt.Errorf("response does not match external root intention")
	}
	identity := response.Response.Identity
	if identity.ID != "" || identity.Kind != "" {
		return Delivery{}, ResponseReservation{}, fmt.Errorf("external response cannot replace authenticated identity")
	}
	reservation, err := request.security.beginResponse(response.Response.Status)
	if err != nil {
		return Delivery{}, ResponseReservation{}, err
	}
	response.Response.To = requestMessage.Intention.From
	response.Response.From = requestMessage.Intention.To
	security := &deliverySecurity{external: context, response: &reservation}
	return Delivery{message: response, security: security}, reservation, nil
}

// BindExternalResponse binds a family response to an admitted external
// request. A response to the signed root acquires the one external response
// permit; a response to a derived child remains internal and follows the
// private return route carried by the request Delivery.
func BindExternalResponse(request Delivery, response circulation.Message) (Delivery, ResponseReservation, error) {
	context, external := request.ExternalAdmission()
	requestMessage, valid := request.Message()
	if !valid || !external || requestMessage.Kind != circulation.ValueKindIntention {
		return Delivery{}, ResponseReservation{}, fmt.Errorf("external request delivery is required")
	}
	if request.security.restricted {
		return replyRestrictedLocal(request, response)
	}
	if requestMessage.Intention.IntentionID == context.RootIntentionID() {
		return BeginExternalResponse(request, response)
	}
	delivery, err := ReplyExternalDerived(request, response)
	return delivery, ResponseReservation{}, err
}

// replyRestrictedLocal answers a restricted internal component. There is nothing
// to sign and no permit: the answer is an ordinary local response, with its
// coordinates forced from the request so a family cannot redirect it.
func replyRestrictedLocal(request Delivery, response circulation.Message) (Delivery, ResponseReservation, error) {
	requestMessage, _ := request.Message()
	if response.Kind != circulation.ValueKindResponse || response.Response.IntentionID != requestMessage.Intention.IntentionID {
		return Delivery{}, ResponseReservation{}, fmt.Errorf("response does not match the restricted request")
	}
	response.Response.To = requestMessage.Intention.From
	response.Response.From = requestMessage.Intention.To
	response.Response.Identity = circulation.Identity{}
	return Delivery{message: response, security: &deliverySecurity{localSource: "restricted:" + request.security.external.PrincipalID()}}, ResponseReservation{}, nil
}

// ReplyExternalDerived constructs the internal response to one derived
// intention. It preserves the admission for direct internal routing but does
// not attach or consume the root's external response permit. Such a response
// can only return to the exact child caller and cannot cross a physical
// boundary as an external reply.
func ReplyExternalDerived(request Delivery, response circulation.Message) (Delivery, error) {
	context, external := request.ExternalAdmission()
	requestMessage, valid := request.Message()
	if !valid || !external || requestMessage.Kind != circulation.ValueKindIntention {
		return Delivery{}, fmt.Errorf("external derived request delivery is required")
	}
	child := requestMessage.Intention
	correlation := child.Correlation
	if child.IntentionID == "" || child.IntentionID == context.RootIntentionID() || correlation == nil ||
		correlation.ParentIntentionID == "" || correlation.RootIntentionID == "" {
		return Delivery{}, fmt.Errorf("request is not bound to an external derived chain")
	}
	return replyExternalDelegated(request, response)
}

// replyExternalDelegated constructs the logical response produced by a
// wrapper for the exact admitted intention retained engine-side. It is private
// because a root response still needs BeginExternalResponse at the signed
// egress adapter; only WrapperDelegationStore may bind wire input to a root.
func replyExternalDelegated(request Delivery, response circulation.Message) (Delivery, error) {
	context, external := request.ExternalAdmission()
	requestMessage, valid := request.Message()
	if !valid || !external || requestMessage.Kind != circulation.ValueKindIntention {
		return Delivery{}, fmt.Errorf("external delegated request delivery is required")
	}
	child := requestMessage.Intention
	if response.Kind != circulation.ValueKindResponse || response.Response.IntentionID != child.IntentionID {
		return Delivery{}, fmt.Errorf("response does not match external delegated intention")
	}
	identity := response.Response.Identity
	if identity.ID != "" || identity.Kind != "" {
		return Delivery{}, fmt.Errorf("external delegated response cannot replace authenticated identity")
	}
	switch response.Response.Status {
	case circulation.ValueStatusRunning, circulation.ValueStatusOK, circulation.ValueStatusError:
	default:
		return Delivery{}, fmt.Errorf("external derived response has invalid status")
	}
	response.Response.To = child.From
	response.Response.From = child.To
	security := &deliverySecurity{external: context, restricted: request.security.restricted}
	if request.security.wrapperBack != nil {
		route := *request.security.wrapperBack
		security.wrapperBack = &route
	}
	return Delivery{message: response, security: security}, nil
}

// MustLocalDelivery is intended for sends made with a runtime-issued fixed
// authority. Invalid authorities indicate a programming error.
func MustLocalDelivery(authority LocalAuthority, message circulation.Message) Delivery {
	delivery, err := NewLocalDelivery(authority, message)
	if err != nil {
		panic(err)
	}
	return delivery
}

// Valid reports whether the delivery carries explicit internal authority.
func (d Delivery) Valid() bool {
	if d.security == nil {
		return false
	}
	local := d.security.localSource != ""
	external := d.security.external != nil && d.security.external.Valid()
	return local != external
}

// Message returns the ordinary application envelope. Security state remains
// attached only to Delivery and cannot leak through serialization.
func (d Delivery) Message() (circulation.Message, bool) {
	if !d.Valid() {
		return circulation.Message{}, false
	}
	return d.message, true
}

// Forward carries an existing authority across an internal routing step.
// Trusted local traffic may be normalized by communication membranes. An
// externally admitted message is instead bound to the exact verified or
// explicitly derived envelope and cannot be rewritten by generic routing.
func Forward(delivery Delivery, message circulation.Message) (Delivery, error) {
	original, valid := delivery.Message()
	if !valid {
		return Delivery{}, fmt.Errorf("valid delivery is required")
	}
	if _, external := delivery.ExternalAdmission(); external && !reflect.DeepEqual(original, message) {
		return Delivery{}, fmt.Errorf("externally admitted message cannot be rewritten by generic routing")
	}
	return Delivery{message: message, security: delivery.security}, nil
}

// LocalAuthoritySource exposes diagnostic provenance without exposing a
// mutable authority handle.
func (d Delivery) LocalAuthoritySource() (string, bool) {
	if !d.Valid() || d.security.localSource == "" {
		return "", false
	}
	return d.security.localSource, true
}

// ExternalAdmission returns immutable read-only admission metadata. Local
// deliveries and invalid deliveries never expose one.
func (d Delivery) ExternalAdmission() (*admission.Context, bool) {
	if !d.Valid() || d.security.external == nil {
		return nil, false
	}
	return d.security.external, true
}

// ExternalResponse returns the response-only reservation attached by
// BeginExternalResponse. An ordinary admitted intention has none.
func (d Delivery) ExternalResponse() (ResponseReservation, bool) {
	if !d.Valid() || d.security.response == nil || !d.security.response.Valid() {
		return ResponseReservation{}, false
	}
	return *d.security.response, true
}

// ExternalWrapperDispatch exposes only the trusted implementation-route name.
// It is absent from ordinary external and all local deliveries.
func (d Delivery) ExternalWrapperDispatch() (string, bool) {
	if !d.Valid() || d.security.external == nil || strings.TrimSpace(d.security.wrapper) == "" {
		return "", false
	}
	return d.security.wrapper, true
}

// ExternalWrapperReturn exposes the trusted return transport selected when a
// wrapper used a delegation handle to create this exact derived call. It is
// absent from roots, ordinary derivations, local traffic, and wire messages.
func (d Delivery) ExternalWrapperReturn() (string, WrapperConnection, WrapperDelegationHandle, bool) {
	if !d.Valid() || d.security.external == nil || d.security.wrapperBack == nil {
		return "", WrapperConnection{}, WrapperDelegationHandle{}, false
	}
	route := d.security.wrapperBack
	if !validWrapperName(route.wrapperName) || !route.connection.Valid() || !route.handle.Valid() {
		return "", WrapperConnection{}, WrapperDelegationHandle{}, false
	}
	return route.wrapperName, route.connection, route.handle, true
}
