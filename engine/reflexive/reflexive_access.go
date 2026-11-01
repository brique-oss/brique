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

package reflexive

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
	errReflexiveExternalInvalidMessage = errors.New("external Reflexive delivery must contain an intention")
	errReflexiveTrustUnavailable       = errors.New("instance trust runtime is unavailable")
	errReflexiveCapabilityUnauthorized = errors.New("Reflexive capability is not authorized")
	errReflexiveResourceUnauthorized   = errors.New("Reflexive resource access is not authorized")
)

// Every reflexive capacity now declares a finite access set. The map stays so
// that a capacity added later and not yet reviewed is an explicit, visible
// decision (parity test): list it here with the reason until it has an extractor.
var deferredReflexiveAccess = map[string]string{}

func buildReflexiveAccessRegistry() (*access.Registry, error) {
	extractors := map[string]access.Extractor{
		"read.meaning":   reflexiveReadMeaningExtractor,
		"read.document":  reflexiveReadDocumentExtractor,
		"read.state":     reflexiveContextExtractor("stat"),
		"read.capacity":  reflexiveReadCapacityExtractor,
		"read.structure": reflexiveContextExtractor("read_recursive"),
		// Maintenance of the projection is the control verb on the context it
		// covers (model §5.3): rebuilding or syncing rewrites the projection of the
		// whole subtree, which reading it does not authorize.
		"meaning.rebuild":  reflexiveContextExtractor("control"),
		"meaning.update":   reflexiveContextExtractor("control"),
		"meaning.query":    reflexiveContextExtractor("read_recursive"),
		"trace.inspect":    reflexiveTraceInspectExtractor,
		"vocabulary.get":   reflexiveVocabularyExtractor,
		"vocabulary.query": reflexiveVocabularyExtractor,
		// The vocabulary is global: changing it is a definition of the root.
		"vocabulary.patch":          reflexiveVocabularyWriteExtractor,
		"vocabulary.delete":         reflexiveVocabularyWriteExtractor,
		"edit.patch_meaning":        reflexiveEditItemsExtractor("write"),
		"edit.delete":               reflexiveEditItemsExtractor("delete"),
		"edit.create":               reflexiveEditItemsExtractor("create"),
		"edit.duplicate":            reflexiveDuplicateExtractor,
		"edit.get_element_template": reflexiveNoResourceExtractor,
	}
	return access.NewFamilyRegistry("reflexive", extractors, access.DefaultMaxAccesses)
}

func reflexiveTraceInspectExtractor(destination shared.ContextAddr, intention circulation.Intention) ([]access.ResourceAccess, error) {
	accesses := []access.ResourceAccess{{Kind: "trace", Context: destination, ID: "events", Action: "read"}}
	// Message content is a separate right: asking for it adds the payloads access.
	if wanted, _ := intention.Params[circulation.KeyIncludePayloads].(bool); wanted {
		accesses = append(accesses, access.ResourceAccess{Kind: "trace", Context: destination, ID: "events", Action: "payloads"})
	}
	return accesses, nil
}

// reflexiveVocabularyExtractor: the vocabulary is derived from the whole
// instance and returned without filtering (all or nothing, D19), so it needs the
// recursive read right on the root, which only a read target on /root/** grants.
// The address is deliberately not trusted: the right is always the root's.
func reflexiveVocabularyExtractor(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
	return []access.ResourceAccess{{Kind: circulation.ValueContext, Context: shared.ContextAddr(shared.RootContextID), Action: "read_recursive"}}, nil
}

// reflexiveVocabularyWriteExtractor: the vocabulary belongs to the root, whatever
// the address, so changing it needs the right to define on the root itself.
func reflexiveVocabularyWriteExtractor(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
	return []access.ResourceAccess{{Kind: circulation.ValueContext, Context: shared.ContextAddr(shared.RootContextID), Action: "write"}}, nil
}

func mustBuildReflexiveAccessRegistry() *access.Registry {
	registry, err := buildReflexiveAccessRegistry()
	if err != nil {
		panic(fmt.Sprintf("build Reflexive resource extractor registry: %v", err))
	}
	return registry
}

func (l *ReflexiveLoop) extractResourceAccesses(in circulation.Intention) ([]access.ResourceAccess, error) {
	if l == nil || l.accessRegistry == nil {
		return nil, fmt.Errorf("Reflexive resource extractor registry is unavailable")
	}
	return l.accessRegistry.Extract(in.To.Cap, shared.ContextAddr(in.To.Context), in)
}

func (l *ReflexiveLoop) authorizeExternalReflexive(delivery junction.Delivery) error {
	if err := l.authorizeExternalReflexiveCapability(delivery); err != nil {
		return err
	}
	admitted, _ := delivery.ExternalAdmission()
	msg, _ := delivery.Message()
	runtime := l.frame.CtxCommReg.(junction.TrustRuntimeProvider).TrustRuntime()
	accesses, err := l.extractResourceAccesses(msg.Intention)
	if err != nil {
		return err
	}
	if !admitted.AuthorizeResources(runtime, accesses) {
		admitted.RecordResourceDenial(junction.AuditOf(l.frame.CtxCommReg), runtime, "wire", msg.Intention, accesses)
		return errReflexiveResourceUnauthorized
	}
	return nil
}

// authorizeExternalReflexiveCapability checks that the delivery is a well formed
// external intention and that its capacity is granted.
func (l *ReflexiveLoop) authorizeExternalReflexiveCapability(delivery junction.Delivery) error {
	admitted, external := delivery.ExternalAdmission()
	msg, valid := delivery.Message()
	if !valid || !external || msg.Kind != circulation.ValueKindIntention {
		return errReflexiveExternalInvalidMessage
	}
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return errReflexiveTrustUnavailable
	}
	provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	if !ok || provider.TrustRuntime() == nil {
		return errReflexiveTrustUnavailable
	}
	runtime := provider.TrustRuntime()
	to := msg.Intention.To
	if !admitted.AuthorizeCapability(runtime, string(to.Context), to.Type, to.Cap) {
		admitted.RecordCapabilityDenial(junction.AuditOf(l.frame.CtxCommReg), runtime, "wire", msg.Intention)
		return errReflexiveCapabilityUnauthorized
	}
	return nil
}

func reflexiveNoResourceExtractor(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
	return []access.ResourceAccess{}, nil
}

func reflexiveContextExtractor(action string) access.Extractor {
	return func(destination shared.ContextAddr, _ circulation.Intention) ([]access.ResourceAccess, error) {
		return []access.ResourceAccess{{Kind: circulation.ValueContext, Context: destination, Action: action}}, nil
	}
}

func reflexiveElementAccess(kind string, context shared.ContextAddr, id, action string) (access.ResourceAccess, error) {
	kind = normType(kind)
	id = strings.TrimSpace(id)
	switch kind {
	case circulation.ValueContext:
		return access.ResourceAccess{Kind: kind, Context: context, Action: action}, nil
	case circulation.ValueCapacity, circulation.ValueSchema, circulation.ValueDocument:
		if id == "" {
			return access.ResourceAccess{}, fmt.Errorf("%s name is required", kind)
		}
		return access.ResourceAccess{Kind: kind, Context: context, ID: id, Action: action}, nil
	case circulation.ValueMatter, circulation.ValueStructure:
		return access.ResourceAccess{}, fmt.Errorf("%s is owned by Matter", kind)
	default:
		return access.ResourceAccess{}, fmt.Errorf("unsupported element kind %q", kind)
	}
}

func reflexiveReadMeaningExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	raw, ok := in.Params[circulation.KeyInput].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("input must be a non-empty array")
	}
	result := make([]access.ResourceAccess, 0, len(raw))
	for index, value := range raw {
		item, ok := value.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("input[%d] must be an object", index)
		}
		kind, _ := item[circulation.KeyElementKind].(string)
		name, _ := item[circulation.KeyElementName].(string)
		resource, err := reflexiveElementAccess(kind, destination, name, "read")
		if err != nil {
			return nil, fmt.Errorf("input[%d]: %w", index, err)
		}
		result = append(result, resource)
	}
	return result, nil
}

func reflexiveReadDocumentExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	raw, ok := in.Params[circulation.KeyItems].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("items must be a non-empty array")
	}
	result := make([]access.ResourceAccess, 0, len(raw))
	for index, value := range raw {
		item, ok := value.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("items[%d] must be an object", index)
		}
		name, _ := item[circulation.KeyName].(string)
		resource, err := reflexiveElementAccess(circulation.ValueDocument, destination, name, "read")
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", index, err)
		}
		result = append(result, resource)
	}
	return result, nil
}

func reflexiveReadCapacityExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	name := strings.TrimSpace(shared.GetParamString(in.Params, "cap_name"))
	resource, err := reflexiveElementAccess(circulation.ValueCapacity, destination, name, "read")
	if err != nil {
		return nil, err
	}
	return []access.ResourceAccess{resource}, nil
}

func reflexiveEditItemsExtractor(action string) access.Extractor {
	return func(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
		items, err := parseItems(in.Params)
		if err != nil {
			return nil, err
		}
		result := make([]access.ResourceAccess, 0, len(items))
		for index, item := range items {
			kind := normType(item.Type)
			if kind == circulation.ValueContext && (action == "create" || action == "delete") {
				child, err := reflexiveChildContext(destination, item.Name)
				if err != nil {
					return nil, fmt.Errorf("items[%d]: %w", index, err)
				}
				result = append(result,
					access.ResourceAccess{Kind: kind, Context: child, Action: action},
					access.ResourceAccess{Kind: kind, Context: destination, Action: "write"},
				)
				continue
			}
			resource, err := reflexiveElementAccess(kind, destination, item.Name, action)
			if err != nil {
				return nil, fmt.Errorf("items[%d]: %w", index, err)
			}
			result = append(result, resource)
		}
		return result, nil
	}
}

func reflexiveDuplicateExtractor(destination shared.ContextAddr, in circulation.Intention) ([]access.ResourceAccess, error) {
	items, err := parseItems(in.Params)
	if err != nil {
		return nil, err
	}
	result := make([]access.ResourceAccess, 0, len(items)*2)
	for index, item := range items {
		kind := normType(item.Type)
		targetContext := destination
		if item.DestinationCtxID != "" {
			targetContext = shared.ContextAddr(item.DestinationCtxID)
		}
		if kind == circulation.ValueContext {
			source, err := reflexiveChildContext(destination, item.SourceName)
			if err != nil {
				return nil, fmt.Errorf("items[%d] source: %w", index, err)
			}
			target, err := reflexiveChildContext(targetContext, item.TargetName)
			if err != nil {
				return nil, fmt.Errorf("items[%d] target: %w", index, err)
			}
			result = append(result,
				access.ResourceAccess{Kind: kind, Context: source, Action: "read"},
				access.ResourceAccess{Kind: kind, Context: target, Action: "create"},
				access.ResourceAccess{Kind: kind, Context: targetContext, Action: "write"},
			)
			continue
		}
		source, err := reflexiveElementAccess(kind, destination, item.SourceName, "read")
		if err != nil {
			return nil, fmt.Errorf("items[%d] source: %w", index, err)
		}
		target, err := reflexiveElementAccess(kind, targetContext, item.TargetName, "create")
		if err != nil {
			return nil, fmt.Errorf("items[%d] target: %w", index, err)
		}
		result = append(result, source, target)
	}
	return result, nil
}

func reflexiveChildContext(parent shared.ContextAddr, child string) (shared.ContextAddr, error) {
	child = strings.TrimSpace(child)
	if child == "" || strings.ContainsAny(child, "/\\") || child == "." || child == ".." {
		return "", fmt.Errorf("child context name must be one non-empty segment")
	}
	return shared.ContextAddr(strings.TrimSuffix(string(parent), "/") + "/" + child), nil
}

// AccessRegistry returns the extractor registry of the Reflexive family, for the
// generated security matrix and for parity checks. It is a fresh, immutable registry.
func AccessRegistry() *access.Registry { return mustBuildReflexiveAccessRegistry() }
