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

package comm

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

type wrapperDelegationTransport interface {
	currentWrapperConnection() (junction.WrapperConnection, bool)
}

func (l *CommLoop) routeExternalWrapperDispatch(delivery junction.Delivery, wrapperName string) {
	message, valid := delivery.Message()
	if !valid || message.Kind != circulation.ValueKindIntention || l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	boundWrapper, marked := delivery.ExternalWrapperDispatch()
	if !marked || boundWrapper != wrapperName {
		l.sendToTrace(message, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCrypto, "invalid external wrapper dispatch metadata")
		return
	}
	boundary, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	if !ok || boundary == "" {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper boundary not found: %s", wrapperName)))
		return
	}
	if shared.ContextAddr(l.frame.CtxId) != boundary {
		l.forwardToContext(boundary, message, delivery)
		return
	}
	l.enqueueExternalWrapperDispatch(delivery, wrapperName, boundary)
}

func (l *CommLoop) enqueueExternalWrapperDispatch(delivery junction.Delivery, wrapperName string, boundary shared.ContextAddr) {
	message, _ := delivery.Message()
	runtime := l.ifaces[wrapperName]
	if runtime == nil || runtime.Type != EndpointWrapper || runtime.WrapperEgress == nil || runtime.State().Status != InterfaceRunning {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper interface unavailable: %s", wrapperName)))
		return
	}
	transport, ok := runtime.Impl.(wrapperDelegationTransport)
	if !ok {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryInvalid, wrapperName, fmt.Errorf("wrapper interface has no delegation transport")))
		return
	}
	connection, ok := transport.currentWrapperConnection()
	if !ok {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper has no active connection")))
		return
	}
	provider, ok := l.frame.CtxCommReg.(junction.WrapperDelegationProvider)
	if !ok || provider.WrapperDelegations() == nil {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryInvalid, wrapperName, fmt.Errorf("wrapper delegation registry unavailable")))
		return
	}
	store := provider.WrapperDelegations()
	handle, err := store.Grant(delivery, wrapperName, connection)
	if err != nil {
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, err))
		return
	}
	projected, ok := projectExternalWrapperMessage(message, wrapperName, boundary)
	if !ok {
		store.Revoke(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryInvalid, wrapperName, fmt.Errorf("invalid wrapper transport projection")))
		return
	}
	item := wrapperInterfaceEgressItem{
		Connection: connection,
		Frame:      junction.WrapperFrame{Message: projected, Delegation: handle},
	}
	select {
	case runtime.WrapperEgress <- item:
		return
	case <-l.done:
		store.Revoke(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryStopped, wrapperName, fmt.Errorf("communication stopped before wrapper dispatch")))
	}
}

// routeExternalWrapperReturn sends a derived response only to the wrapper
// connection retained in the private Delivery sidecar. Neither the logical
// response address nor any wrapper-provided field can select this route.
func (l *CommLoop) routeExternalWrapperReturn(delivery junction.Delivery, wrapperName string, connection junction.WrapperConnection, handle junction.WrapperDelegationHandle) {
	message, valid := delivery.Message()
	if !valid || message.Kind != circulation.ValueKindResponse || l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	boundWrapper, boundConnection, boundHandle, marked := delivery.ExternalWrapperReturn()
	if !marked || boundWrapper != wrapperName || boundConnection != connection || boundHandle != handle {
		l.sendToTrace(message, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCrypto, "invalid external wrapper return metadata")
		return
	}
	boundary, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	if !ok || boundary == "" {
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper boundary not found: %s", wrapperName)))
		return
	}
	if shared.ContextAddr(l.frame.CtxId) != boundary {
		l.forwardToContext(boundary, message, delivery)
		return
	}
	l.enqueueExternalWrapperReturn(delivery, wrapperName, boundary, connection, handle)
}

func (l *CommLoop) enqueueExternalWrapperReturn(delivery junction.Delivery, wrapperName string, boundary shared.ContextAddr, connection junction.WrapperConnection, handle junction.WrapperDelegationHandle) {
	message, _ := delivery.Message()
	runtime := l.ifaces[wrapperName]
	if runtime == nil || runtime.Type != EndpointWrapper || runtime.WrapperEgress == nil || runtime.State().Status != InterfaceRunning {
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper interface unavailable: %s", wrapperName)))
		return
	}
	transport, ok := runtime.Impl.(wrapperDelegationTransport)
	if !ok {
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryInvalid, wrapperName, fmt.Errorf("wrapper interface has no delegation transport")))
		return
	}
	current, connected := transport.currentWrapperConnection()
	if !connected || current != connection {
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryUnavailable, wrapperName, fmt.Errorf("wrapper connection changed before delegated response")))
		return
	}
	projected, ok := projectExternalWrapperResponse(message, wrapperName, boundary)
	if !ok {
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryInvalid, wrapperName, fmt.Errorf("invalid wrapper response transport projection")))
		return
	}
	item := wrapperInterfaceEgressItem{
		Connection: connection,
		Frame:      junction.WrapperFrame{Message: projected, Delegation: handle},
	}
	select {
	case runtime.WrapperEgress <- item:
		return
	case <-l.done:
		l.revokeWrapperDelegation(handle)
		l.reportDeliveryFailure(message, newDeliveryResult(deliveryStopped, wrapperName, fmt.Errorf("communication stopped before delegated wrapper response")))
	}
}

func (l *CommLoop) revokeWrapperDelegation(handle junction.WrapperDelegationHandle) {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	provider, ok := l.frame.CtxCommReg.(junction.WrapperDelegationProvider)
	if ok && provider.WrapperDelegations() != nil {
		provider.WrapperDelegations().Revoke(handle)
	}
}

func projectExternalWrapperMessage(message circulation.Message, wrapperName string, boundary shared.ContextAddr) (circulation.Message, bool) {
	if message.Kind != circulation.ValueKindIntention || strings.TrimSpace(wrapperName) == "" || boundary == "" {
		return circulation.Message{}, false
	}
	original := strings.TrimSpace(string(message.Intention.To.Context))
	if original == "" || original[0] != '/' {
		return circulation.Message{}, false
	}
	prefix := strings.TrimSuffix(string(boundary), "/")
	var relative string
	switch {
	case original == prefix:
	case strings.HasPrefix(original, prefix+"/"):
		relative = strings.TrimPrefix(original, prefix+"/")
	default:
		relative = strings.TrimPrefix(original, "/")
	}
	message.Intention.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + relative)
	return message, true
}

func projectExternalWrapperResponse(message circulation.Message, wrapperName string, boundary shared.ContextAddr) (circulation.Message, bool) {
	if message.Kind != circulation.ValueKindResponse || strings.TrimSpace(wrapperName) == "" || boundary == "" {
		return circulation.Message{}, false
	}
	original := strings.TrimSpace(string(message.Response.To.Context))
	prefix := strings.TrimSuffix(string(boundary), "/")
	if original != prefix && !strings.HasPrefix(original, prefix+"/") {
		return circulation.Message{}, false
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(original, prefix), "/")
	message.Response.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + relative)
	return message, true
}

func (l *CommLoop) resolveWrapperDelegatedIngress(item IngressItem) bool {
	resolved, _ := l.resolveWrapperDelegatedIngressDetailed(item)
	return resolved
}

func (l *CommLoop) resolveWrapperDelegatedIngressDetailed(item IngressItem) (bool, error) {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil || item.Endpoint != EndpointWrapper ||
		!item.WrapperConnection.Valid() || !item.WrapperDelegation.Valid() ||
		(item.Msg.Kind != circulation.ValueKindIntention && item.Msg.Kind != circulation.ValueKindResponse) {
		return false, fmt.Errorf("invalid delegated wrapper ingress metadata")
	}
	provider, ok := l.frame.CtxCommReg.(junction.WrapperDelegationProvider)
	if !ok || provider.WrapperDelegations() == nil {
		return false, fmt.Errorf("wrapper delegation registry unavailable")
	}
	if item.Msg.Kind == circulation.ValueKindResponse {
		// Apply the control-plane bound before a terminal response can revoke
		// its handle. Oversized responses become the standard small correlated
		// error envelope and are then bound like any other wrapper response.
		response, allowed := l.enforceControlMessageLimit(item.Msg)
		if !allowed {
			return false, fmt.Errorf("delegated wrapper response exceeds the control-message limit")
		}
		resolution, err := provider.WrapperDelegations().ResolveResponse(
			item.WrapperDelegation, item.IfaceName, item.WrapperConnection, response,
		)
		if err != nil {
			return false, err
		}
		responseDelivery, valid := resolution.Response()
		if !valid {
			return false, fmt.Errorf("delegated wrapper response resolution is invalid")
		}
		response, valid = responseDelivery.Message()
		if !valid {
			return false, fmt.Errorf("delegated wrapper response delivery is invalid")
		}
		if rootRequest, root := resolution.RootRequest(); root {
			if _, restricted := rootRequest.RestrictedLocal(); restricted {
				// A restricted internal component is answered locally: there is no
				// signed envelope and no permit to acquire.
				local, _, err := junction.BindExternalResponse(rootRequest, response)
				if err != nil {
					l.sendToTrace(response, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto,
						"restricted wrapper root response could not be bound to its request")
					return true, nil
				}
				l.routeIngress(response, item.Endpoint, item.IfaceName, local)
				return true, nil
			}
			signed, _, err := junction.BeginExternalResponse(rootRequest, response)
			if err != nil {
				l.sendToTrace(response, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto,
					"external wrapper root response could not acquire its signed response permit")
				return true, nil
			}
			if shared.IsRootContext(l.frame.CtxId) {
				l.routeSignedExternalResponse(signed)
			} else {
				l.forwardToRoot(response, signed)
			}
			return true, nil
		}
		l.routeIngress(response, item.Endpoint, item.IfaceName, responseDelivery)
		return true, nil
	}
	trustProvider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || trustProvider.TrustRuntime() == nil {
		return false, fmt.Errorf("trust runtime unavailable")
	}
	message := item.Msg
	if !projectDelegatedWrapperOrigin(&message, l.frame.CtxId) {
		return false, fmt.Errorf("invalid delegated wrapper origin")
	}
	derived, err := provider.WrapperDelegations().Resolve(
		item.WrapperDelegation, item.IfaceName, item.WrapperConnection, message, trustProvider.TrustRuntime(),
	)
	if err != nil {
		return false, err
	}
	l.routeIngress(message, item.Endpoint, item.IfaceName, derived)
	return true, nil
}

func projectDelegatedWrapperOrigin(message *circulation.Message, boundary string) bool {
	if message == nil || message.Kind != circulation.ValueKindIntention || boundary == "" {
		return false
	}
	original := string(message.Intention.From.Context)
	raw := strings.TrimSpace(original)
	if raw != original || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "@") ||
		strings.HasSuffix(raw, "/") || strings.Contains(raw, "\\") {
		return false
	}
	if raw == "" {
		message.Intention.From.Context = circulation.ContextID(strings.TrimSuffix(boundary, "/"))
		return true
	}
	segments := strings.Split(raw, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, char := range segment {
			if unicode.IsControl(char) || unicode.IsSpace(char) {
				return false
			}
		}
	}
	message.Intention.From.Context = circulation.ContextID(strings.TrimSuffix(boundary, "/") + "/" + strings.Join(segments, "/"))
	return true
}

// refuseDelegatedWrapperCall answers a wrapper whose derived call the caller's rights do
// not cover. The refusal is the canonical one, which does not say whether the capacity
// exists, and it travels on the delegation the call came in on; without it the wrapper
// would wait for its whole timeout for an answer that never comes.
func (l *CommLoop) refuseDelegatedWrapperCall(item IngressItem, problem *circulation.ResponseProblem) {
	if l == nil || l.frame == nil || item.Msg.Kind != circulation.ValueKindIntention || !item.Msg.Intention.AwaitResponse {
		return
	}
	request := item.Msg
	if !projectDelegatedWrapperOrigin(&request, l.frame.CtxId) {
		return
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, TS: time.Now().UTC().Format(time.RFC3339Nano), Response: circulation.Response{
		IntentionID: request.Intention.IntentionID, To: request.Intention.From, From: request.Intention.To,
		Status: circulation.ValueStatusError, Error: problem,
	}}
	projected, ok := projectExternalWrapperResponse(response, item.IfaceName, shared.ContextAddr(l.frame.CtxId))
	runtime := l.ifaces[item.IfaceName]
	if !ok || runtime == nil || runtime.Type != EndpointWrapper || runtime.WrapperEgress == nil || runtime.State().Status != InterfaceRunning {
		return
	}
	select {
	case runtime.WrapperEgress <- wrapperInterfaceEgressItem{
		Connection: item.WrapperConnection,
		Frame:      junction.WrapperFrame{Message: projected, Delegation: item.WrapperDelegation},
	}:
	case <-l.done:
	}
}
