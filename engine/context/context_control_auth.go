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

package context

// context_control_auth.go
//
// The lifecycle door (SECURITY.md §5, §8).
// Control never accepts a signed external admission: starting and stopping
// contexts is for the instance's own components. A restricted internal component
// (a wrapper with a local.<name> principal) may do it, and only on the contexts
// its principal holds the control verb on: the door checks that the admission is
// a restricted local one, then the capacity, then the resource context/control of
// each context concerned.

import (
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/access"
	"brique_engine/security/refusal"
	"brique_engine/shared"
)

// controlScope carries what the door decided about a restricted request: the
// children the caller may not control. A nil scope means a trusted caller.
type controlScope struct {
	denied map[string]bool
}

func (scope *controlScope) refused(name string) bool {
	return scope != nil && scope.denied[name]
}

// controlRequest turns a delivery into the control message to run. Trusted local
// deliveries pass; signed external ones never do; a restricted local one passes
// only through the door below.
func (c *ContextLoop) controlRequest(delivery junction.Delivery) (circulation.Message, *controlScope, bool) {
	msg, valid := delivery.Message()
	if !valid {
		return circulation.Message{}, nil, false
	}
	if _, external := delivery.ExternalAdmission(); !external {
		return msg, nil, true
	}
	if _, restricted := delivery.RestrictedLocal(); !restricted || msg.Kind != circulation.ValueKindIntention {
		junction.TraceEmit(&c.frame, circulation.TraceWire{
			TraceKind:   circulation.ValueTraceFamilyError,
			Family:      circulation.ValueTypeControl,
			IntentionId: msg.Intention.IntentionID,
			MsgKind:     msg.Kind,
			ReasonCode:  circulation.ValueReasonInvalidCrypto,
			UserText:    "external control admission is not activated",
		})
		return circulation.Message{}, nil, false
	}
	return c.admitRestrictedControl(delivery, msg)
}

func (c *ContextLoop) admitRestrictedControl(delivery junction.Delivery, msg circulation.Message) (circulation.Message, *controlScope, bool) {
	in := msg.Intention
	admitted, _ := delivery.RestrictedLocal()
	provider, ok := c.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		c.respondProblem(in, refusal.CapabilityDenied(circulation.ValueOriginControl))
		return circulation.Message{}, nil, false
	}
	runtime := provider.TrustRuntime()
	recorder := junction.AuditOf(c.frame.CtxCommReg)
	if _, known := access.ClassOf(circulation.ValueTypeControl, in.To.Cap); !known {
		admitted.RecordCapabilityDenial(recorder, runtime, "wrapper", in)
		c.respondProblem(in, refusal.CapabilityDenied(circulation.ValueOriginControl))
		return circulation.Message{}, nil, false
	}
	here := shared.ContextAddr(c.frame.CtxId)
	child := func(name string) shared.ContextAddr {
		return shared.ContextAddr(strings.TrimRight(string(here), "/") + "/" + name)
	}
	var children []string
	if in.To.Cap != "stop" { // "stop" alone stops the addressed context itself
		var err error
		if children, err = parseControlChildren(in.Params); err != nil {
			c.respondControlError(in, circulation.ValueCodeInvalid, err.Error(), nil)
			return circulation.Message{}, nil, false
		}
	}
	// The capacity is reached by holding the control verb on a context it will act
	// on: the addressed one, or one of the children named. Holding it nowhere is a
	// refusal of the capacity itself.
	held := admitted.AuthorizeVerb(runtime, string(here), "control")
	for _, name := range children {
		held = held || (validChildName(name) && admitted.AuthorizeVerb(runtime, string(child(name)), "control"))
	}
	if !held {
		admitted.RecordCapabilityDenial(recorder, runtime, "wrapper", in)
		c.respondProblem(in, refusal.CapabilityDenied(circulation.ValueOriginControl))
		return circulation.Message{}, nil, false
	}
	if in.To.Cap == "stop" {
		accesses := []access.ResourceAccess{{Kind: circulation.ValueContext, Context: here, Action: "control"}}
		if !admitted.AuthorizeResources(runtime, accesses) {
			admitted.RecordResourceDenial(recorder, runtime, "wrapper", in, accesses)
			c.respondProblem(in, refusal.NotFound(circulation.ValueOriginControl))
			return circulation.Message{}, nil, false
		}
		return msg, &controlScope{}, true
	}
	scope := &controlScope{denied: map[string]bool{}}
	for _, name := range children {
		if !validChildName(name) {
			continue // reported by the operation itself, whatever the rights
		}
		accesses := []access.ResourceAccess{{Kind: circulation.ValueContext, Context: child(name), Action: "control"}}
		if !admitted.AuthorizeResources(runtime, accesses) {
			admitted.RecordResourceDenial(recorder, runtime, "wrapper", in, accesses)
			scope.denied[name] = true
		}
	}
	return msg, scope, true
}

// respondProblem answers a control intention with a canonical refusal.
func (c *ContextLoop) respondProblem(in circulation.Intention, problem *circulation.ResponseProblem) {
	c.respond(in, circulation.Response{Status: circulation.ValueStatusError, Error: problem})
}
