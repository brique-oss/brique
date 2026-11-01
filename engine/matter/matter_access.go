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

package matter

import (
	"errors"
	"fmt"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/access"
	"brique_engine/shared"
)

var (
	errMatterExternalInvalidMessage = errors.New("external Matter delivery must contain an intention")
	errMatterTrustUnavailable       = errors.New("instance trust runtime is unavailable")
	errMatterCapabilityUnauthorized = errors.New("Matter capability is not authorized")
	errMatterResourceUnauthorized   = errors.New("Matter resource access is not authorized")
)

// buildMatterAccessRegistry is deliberately separate from the handler table:
// local calls keep their existing semantics while externally admitted calls
// remain unavailable until the authorized response path is connected. A
// missing extractor is an explicit fail-closed decision.
func buildMatterAccessRegistry() (*access.Registry, error) {
	extractors := map[string]access.Extractor{
		"matter.create":     createExtractor("matter", circulation.KeyMatterID),
		"matter.delete":     resourceParamExtractor("matter", circulation.KeyMatterID, "delete"),
		"matter.clone":      cloneExtractor("matter", circulation.KeySourceMatterID, circulation.KeyTargetMatterID),
		"matter.derive":     matterDeriveExtractor,
		"structure.create":  createExtractor("structure", circulation.KeyStructureID),
		"structure.delete":  resourceParamExtractor("structure", circulation.KeyStructureID, "delete"),
		"structure.clone":   cloneExtractor("structure", circulation.KeySourceStructureID, circulation.KeyTargetStructureID),
		"structure.derive":  createExtractor("structure", circulation.KeyTargetStructureID),
		"matter.read":       resourceParamExtractor("matter", circulation.KeyMatterID, "read"),
		"matter.stat":       resourceParamExtractor("matter", circulation.KeyMatterID, "stat"),
		"matter.exists":     resourceParamExtractor("matter", circulation.KeyMatterID, "stat"),
		"matter.read_batch": matterReadBatchExtractor,
		"structure.read":    resourceParamExtractor("structure", circulation.KeyStructureID, "read"),
		"matter.write":      resourceParamExtractor("matter", circulation.KeyMatterID, "write"),
		"matter.append":     resourceParamExtractor("matter", circulation.KeyMatterID, "append"),
		"matter.truncate":   resourceParamExtractor("matter", circulation.KeyMatterID, "truncate"),
		"structure.patch":   resourceParamExtractor("structure", circulation.KeyStructureID, "write"),
		// Subscribing is reading: the caller must hold matter/read on the matter.
		"matter.subscribe": resourceParamExtractor("matter", circulation.KeyMatterID, "read"),
		// Unsubscribing and acknowledging touch no world resource: they act on
		// the caller's OWN subscription, which the handler checks by owner and
		// answers as "absent" to anyone else. Requiring a right here would
		// strand a subscriber whose right was just withdrawn.
		"matter.unsubscribe":      subscriptionOwnerExtractor,
		"matter.subscription.ack": subscriptionOwnerExtractor,
	}
	return access.NewFamilyRegistry("matter", extractors, access.DefaultMaxAccesses)
}

func subscriptionOwnerExtractor(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
	return []access.ResourceAccess{}, nil
}

func mustBuildMatterAccessRegistry() *access.Registry {
	registry, err := buildMatterAccessRegistry()
	if err != nil {
		panic(fmt.Sprintf("build Matter resource extractor registry: %v", err))
	}
	return registry
}

func (l *MatterLoop) extractResourceAccesses(in circulation.Intention) ([]access.ResourceAccess, error) {
	if l == nil || l.accessRegistry == nil {
		return nil, fmt.Errorf("Matter resource extractor registry is unavailable")
	}
	return l.accessRegistry.Extract(in.To.Cap, shared.ContextAddr(in.To.Context), in)
}

// authorizeExternalMatter performs the complete pre-effect resource check for
// one admitted intention. It deliberately does not dispatch: the caller must
// also have an authorized external response path before allowing handlers to
// run.
func (l *MatterLoop) authorizeExternalMatter(delivery junction.Delivery) error {
	admitted, external := delivery.ExternalAdmission()
	msg, valid := delivery.Message()
	if !valid || !external || msg.Kind != circulation.ValueKindIntention {
		return errMatterExternalInvalidMessage
	}
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return errMatterTrustUnavailable
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return errMatterTrustUnavailable
	}
	runtime := provider.TrustRuntime()
	to := msg.Intention.To
	recorder := junction.AuditOf(l.frame.CtxCommReg)
	if !admitted.AuthorizeCapability(runtime, string(to.Context), to.Type, to.Cap) {
		admitted.RecordCapabilityDenial(recorder, runtime, "wire", msg.Intention)
		return errMatterCapabilityUnauthorized
	}
	if admitted.OwnerID() != "" && to.Cap == "matter.subscribe" {
		// A subscription outlives the call and is re-decided against its owner's
		// rights; a façade's authority exists for the call only.
		admitted.RecordCapabilityDenial(recorder, runtime, "wire", msg.Intention)
		return errMatterCapabilityUnauthorized
	}
	accesses, err := l.extractResourceAccesses(msg.Intention)
	if err != nil {
		return err
	}
	// A multi-element read decides each element on its own: an element the caller
	// may not read is reported exactly like one that does not exist, so refusing
	// the whole request here would tell the two apart.
	if to.Cap == "matter.read_batch" {
		return nil
	}
	if !admitted.AuthorizeResources(runtime, accesses) {
		admitted.RecordResourceDenial(recorder, runtime, "wire", msg.Intention, accesses)
		return errMatterResourceUnauthorized
	}
	return nil
}

func requiredAccessParam(in circulation.Intention, key string) (string, error) {
	value := strings.TrimSpace(shared.GetParamString(in.Params, key))
	if value == "" {
		return "", fmt.Errorf("%s is required for external resource authorization", key)
	}
	return value, nil
}

func resourceParamExtractor(kind, key, action string) access.Extractor {
	return func(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
		id, err := requiredAccessParam(in, key)
		if err != nil {
			return nil, err
		}
		return []access.ResourceAccess{{Kind: kind, Context: destination, ID: id, Action: action}}, nil
	}
}

func createExtractor(kind, key string) access.Extractor {
	return resourceParamExtractor(kind, key, "create")
}

func cloneExtractor(kind, sourceKey, targetKey string) access.Extractor {
	return func(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
		source, err := requiredAccessParam(in, sourceKey)
		if err != nil {
			return nil, err
		}
		target, err := requiredAccessParam(in, targetKey)
		if err != nil {
			return nil, err
		}
		targetContext := destination
		if raw := strings.TrimSpace(shared.GetParamString(in.Params, circulation.KeyDestinationCtxId)); raw != "" {
			targetContext = shared.ContextAddr(raw)
		}
		return []access.ResourceAccess{
			{Kind: kind, Context: destination, ID: source, Action: "read"},
			{Kind: kind, Context: targetContext, ID: target, Action: "create"},
		}, nil
	}
}

func matterReadBatchExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	ids, err := strictStringListParam(in.Params, circulation.KeyMatterIDs)
	if err != nil {
		return nil, err
	}
	result := make([]access.ResourceAccess, 0, len(ids))
	for _, id := range ids {
		result = append(result, access.ResourceAccess{Kind: "matter", Context: destination, ID: id, Action: "read"})
	}
	return result, nil
}

func matterDeriveExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	target, err := requiredAccessParam(in, circulation.KeyTargetMatterID)
	if err != nil {
		return nil, err
	}
	result := make([]access.ResourceAccess, 0, 1)
	if raw, present := in.Params[circulation.KeySourceGuards]; present {
		guards, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", circulation.KeySourceGuards)
		}
		for index, rawGuard := range guards {
			guard, ok := rawGuard.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be an object", circulation.KeySourceGuards, index)
			}
			id := strings.TrimSpace(shared.GetParamString(guard, circulation.KeyMatterID))
			if id == "" {
				return nil, fmt.Errorf("%s[%d].%s is required", circulation.KeySourceGuards, index, circulation.KeyMatterID)
			}
			result = append(result, access.ResourceAccess{Kind: "matter", Context: destination, ID: id, Action: "read"})
		}
	}
	result = append(result, access.ResourceAccess{Kind: "matter", Context: destination, ID: target, Action: "create"})
	return result, nil
}

func strictStringListParam(params map[string]any, key string) ([]string, error) {
	if params == nil {
		return nil, fmt.Errorf("%s is required", key)
	}
	raw, present := params[key]
	if !present {
		return nil, fmt.Errorf("%s is required", key)
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		values = make([]string, len(typed))
		for index, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", key, index)
			}
			values[index] = value
		}
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s must not be empty", key)
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if values[index] == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", key, index)
		}
	}
	return values, nil
}

// AccessRegistry returns the extractor registry of the Matter family, for the
// generated security matrix and for parity checks. It is a fresh, immutable registry.
func AccessRegistry() *access.Registry { return mustBuildMatterAccessRegistry() }
