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

package execution

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

type DSLDescriptor struct {
	CapName     string
	RootName    string
	RootSection DSLSection
}

type DSLSection struct {
	Name                   string
	Role                   string
	Inputs                 map[string]any
	Outputs                map[string]any
	Effects                map[string]any
	TransformationContract DSLTransformationContract
	Resolution             map[string]any
}

type DSLTransformationContract struct {
	From      []string
	To        []string
	Affecting []string
	Morphing  string
}

var dslRuntimeNodeKeys = map[string]struct{}{
	"invoke":    {},
	"output":    {},
	">sequence": {},
	">parallel": {},
	">if":       {},
	">switch":   {},
	">for_each": {},
	">while":    {},
	">race":     {},
}

// runDSLOrchestrator
//
// Functional role (Brique DSL):
// - load, validate, and execute a DSL-style user intention orchestration plan.
//
//
// Expected Message Fields:
// - `kind`
// - `intention`
// - `intention.intention_id`
// - `intention.to.cap`
//
// Expected Params Keys/values:
// - none directly; root input params are loaded into the DSL runtime state.
//
// Produced Response Fields:
// - Valid:
//   - exactly one terminal response when the runtime is fully implemented.
// - On error:
//   - one standardized execution error response.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - terminal DSL payload once node execution is implemented.
// - On error:
//   - standardized error details.
//
// Produced Trace:
// - Valid:
//   - none directly in the current loader/validator phase.
// - On error:
//   - the paired execution family-error trace emitted by `emitResponseError`.
//
// Produced Outbound Message:
// - Valid:
//   - none yet in the current loader/validator phase.
// - On error:
//   - one response to Comm.
//
// State/Storage Effects:
// - reads one capacity descriptor from disk.
// - allocates one per-run DSL runtime state.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Fails closed on malformed or non-executable DSL descriptors.
// - In the current phase, execution stops after successful load/validation and returns a `no_orchestration` error until node execution is wired.

func (l *ExecutionLoop) runDSLOrchestrator(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	desc, err := l.loadDSLDescriptor(in.To.Cap)
	if err != nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidConfiguration},
			err.Error(),
		))
		return
	}
	if err := validateDSLPlan(desc); err != nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidConfiguration},
			err.Error(),
		))
		return
	}
	if _, _, ok := firstDSLRuntimeNode(desc.RootSection.Resolution); !ok {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonNoOrchestration},
			"dsl root resolution has no executable runtime node",
		))
		return
	}

	st := newDSLExecState(in, desc.RootSection.Inputs)
	resp, err := l.evalDSLNode(st, desc.RootSection.Resolution)
	if err != nil {
		l.emitResponseError(errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonNoOrchestration},
			err.Error(),
		))
		return
	}
	l.emitToComm(circulation.Message{
		Kind:     circulation.ValueKindResponse,
		Response: rewriteDSLTerminalResponse(in, resp),
	})
}

func (l *ExecutionLoop) loadDSLDescriptor(capName string) (DSLDescriptor, error) {
	capName = strings.TrimSpace(capName)
	if capName == "" {
		return DSLDescriptor{}, fmt.Errorf("dsl cap_name is required")
	}
	if strings.TrimSpace(l.capRoot) == "" {
		return DSLDescriptor{}, fmt.Errorf("execution capRoot is not configured")
	}

	path := filepath.Join(l.capRoot, capName+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return DSLDescriptor{}, fmt.Errorf("failed to read dsl descriptor: %w", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return DSLDescriptor{}, fmt.Errorf("failed to decode dsl descriptor")
	}

	syn := getBriqueSection(doc)
	if syn == nil {
		return DSLDescriptor{}, fmt.Errorf("dsl descriptor is missing brique section")
	}
	if got := synStr(syn, configuration.KeyCapName); got == "" || got != capName {
		return DSLDescriptor{}, fmt.Errorf("dsl descriptor cap_name mismatch")
	}

	functional, _ := doc["functional"].(map[string]any)
	if functional == nil {
		return DSLDescriptor{}, fmt.Errorf("dsl descriptor is missing functional section")
	}

	rootName, rootRaw, err := extractDSLRootSection(functional)
	if err != nil {
		return DSLDescriptor{}, err
	}
	rootSection, err := parseDSLSection(rootName, rootRaw)
	if err != nil {
		return DSLDescriptor{}, err
	}

	return DSLDescriptor{
		CapName:     capName,
		RootName:    rootName,
		RootSection: rootSection,
	}, nil
}

func extractDSLRootSection(functional map[string]any) (string, map[string]any, error) {
	var (
		rootName string
		rootRaw  map[string]any
	)
	for k, v := range functional {
		if strings.HasPrefix(strings.TrimSpace(k), "_") {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok || m == nil {
			return "", nil, fmt.Errorf("dsl root section %q must be an object", k)
		}
		if rootName != "" {
			return "", nil, fmt.Errorf("dsl functional section must contain exactly one root section")
		}
		rootName = k
		rootRaw = m
	}
	if rootName == "" || rootRaw == nil {
		return "", nil, fmt.Errorf("dsl functional section has no root section")
	}
	return rootName, rootRaw, nil
}

func parseDSLSection(name string, raw map[string]any) (DSLSection, error) {
	if strings.TrimSpace(name) == "" {
		return DSLSection{}, fmt.Errorf("dsl root section name is required")
	}
	if raw == nil {
		return DSLSection{}, fmt.Errorf("dsl root section %q is nil", name)
	}

	inputs, _ := raw["inputs"].(map[string]any)
	outputs, _ := raw["outputs"].(map[string]any)
	effects, _ := raw["effects"].(map[string]any)

	tcRaw, _ := raw["transformation_contract"].(map[string]any)
	tc := DSLTransformationContract{}
	if tcRaw != nil {
		tc.From = anyStringSlice(tcRaw["from"])
		tc.To = anyStringSlice(tcRaw["to"])
		tc.Affecting = anyStringSlice(tcRaw["affecting"])
		tc.Morphing = anyString(tcRaw["morphing"])
	}

	resolution, _ := raw["resolution"].(map[string]any)

	return DSLSection{
		Name:    name,
		Role:    anyString(raw["role"]),
		Inputs:  cloneMap(inputs),
		Outputs: cloneMap(outputs),
		Effects: cloneMap(effects),
		TransformationContract: DSLTransformationContract{
			From:      tc.From,
			To:        tc.To,
			Affecting: tc.Affecting,
			Morphing:  tc.Morphing,
		},
		Resolution: cloneMap(resolution),
	}, nil
}

func validateDSLPlan(desc DSLDescriptor) error {
	if strings.TrimSpace(desc.CapName) == "" {
		return fmt.Errorf("dsl descriptor cap_name is required")
	}
	if strings.TrimSpace(desc.RootName) == "" {
		return fmt.Errorf("dsl descriptor root section is required")
	}
	if desc.RootSection.Name != desc.RootName {
		return fmt.Errorf("dsl root section name mismatch")
	}
	if strings.TrimSpace(desc.RootSection.Role) == "" {
		return fmt.Errorf("dsl root section role is required")
	}
	if desc.RootSection.Inputs == nil {
		return fmt.Errorf("dsl root section inputs are required")
	}
	if desc.RootSection.Outputs == nil {
		return fmt.Errorf("dsl root section outputs are required")
	}
	if strings.TrimSpace(desc.RootSection.TransformationContract.Morphing) == "" {
		return fmt.Errorf("dsl transformation_contract.morphing is required")
	}
	if desc.RootSection.Resolution == nil {
		return nil
	}
	nodeKey, nodeRaw, ok := firstDSLRuntimeNode(desc.RootSection.Resolution)
	if !ok {
		return nil
	}
	if _, ok := dslRuntimeNodeKeys[nodeKey]; !ok {
		return fmt.Errorf("unsupported dsl runtime node: %s", nodeKey)
	}
	nodeMap, ok := nodeRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("dsl runtime node %s must be an object", nodeKey)
	}
	if err := validateDSLNode(nodeKey, nodeMap); err != nil {
		return err
	}
	return nil
}

func validateDSLNode(nodeKey string, node map[string]any) error {
	switch nodeKey {
	case "invoke":
		await := true
		if raw, ok := node["await_response"].(bool); ok {
			await = raw
		}
		if !await {
			if anyString(node["as"]) != "" {
				return fmt.Errorf("dsl invoke cannot use as when await_response=false")
			}
			if node["retry"] != nil {
				return fmt.Errorf("dsl invoke cannot use retry when await_response=false")
			}
		}
		capRaw := node["@capacity"]
		capStr := anyString(capRaw)
		if capStr == "" {
			return fmt.Errorf("dsl invoke requires @capacity")
		}
		// allow $input.* and $alias.* as valid capacity refs (resolved at runtime)
		if !strings.HasPrefix(capStr, "$") && !strings.HasPrefix(capStr, "/") && !strings.HasPrefix(capStr, "@ext_") {
			return fmt.Errorf("dsl @capacity must be an absolute ref or a runtime ref ($)")
		}
		if m, ok := node["timeout"].(map[string]any); ok && m != nil && parseDSLInt(m["ms"]) < 0 {
			return fmt.Errorf("dsl invoke timeout.ms must be >= 0")
		}
		if m, ok := node["retry"].(map[string]any); ok && m != nil {
			if parseDSLInt(m["max"]) < 0 || parseDSLInt(m["delay_ms"]) < 0 {
				return fmt.Errorf("dsl invoke retry values must be >= 0")
			}
		}
	case ">parallel", ">for_each":
		if node["limit"] != nil && parseDSLInt(node["limit"]) < 0 {
			return fmt.Errorf("dsl %s limit must be >= 0", nodeKey)
		}
	}
	return nil
}

func firstDSLRuntimeNode(raw map[string]any) (string, any, bool) {
	if raw == nil {
		return "", nil, false
	}
	var (
		foundKey string
		foundVal any
	)
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" || strings.HasPrefix(k, "_") {
			continue
		}
		if _, ok := dslRuntimeNodeKeys[k]; !ok {
			continue
		}
		if foundKey != "" {
			return "", nil, false
		}
		foundKey = k
		foundVal = v
	}
	if foundKey == "" {
		return "", nil, false
	}
	return foundKey, foundVal, true
}

func anyString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func anyStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok || raw == nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAliases(in map[string]circulation.Response) map[string]circulation.Response {
	if in == nil {
		return nil
	}
	out := make(map[string]circulation.Response, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneDSLExecState(in *dslExecState) *dslExecState {
	if in == nil {
		return nil
	}
	out := &dslExecState{
		in:      in.in,
		input:   cloneMap(in.input),
		aliases: cloneAliases(in.aliases),
	}
	if len(in.localScopes) > 0 {
		out.localScopes = make([]dslLocalScope, 0, len(in.localScopes))
		for _, scope := range in.localScopes {
			out.localScopes = append(out.localScopes, dslLocalScope{values: cloneMap(scope.values)})
		}
	}
	return out
}

func (l *ExecutionLoop) evalDSLNode(st *dslExecState, raw map[string]any) (circulation.Response, error) {
	// Check for a sub-section node (#name key) before looking for runtime nodes.
	// A sub-section is an item whose single non-underscore key starts with "#".
	if sectionName, sectionRaw, ok := firstDSLSubSection(raw); ok {
		return l.evalDSLSubSection(st, sectionName, sectionRaw)
	}

	nodeKey, nodeVal, ok := firstDSLRuntimeNode(raw)
	if !ok {
		return circulation.Response{}, fmt.Errorf("dsl runtime node is missing")
	}
	node, ok := nodeVal.(map[string]any)
	if !ok || node == nil {
		return circulation.Response{}, fmt.Errorf("dsl node %s must be an object", nodeKey)
	}

	switch nodeKey {
	case "invoke":
		return l.evalDSLInvokeNode(st, node)
	case "output":
		return evalDSLOutputNode(st, node)
	case ">sequence":
		return l.evalDSLSequenceNode(st, node)
	case ">if":
		return l.evalDSLIfNode(st, node)
	case ">switch":
		return l.evalDSLSwitchNode(st, node)
	case ">parallel":
		return l.evalDSLParallelNode(st, node)
	case ">for_each":
		return l.evalDSLForEachNode(st, node)
	case ">while":
		return l.evalDSLWhileNode(st, node)
	case ">race":
		return l.evalDSLRaceNode(st, node)
	default:
		return circulation.Response{}, fmt.Errorf("dsl node %s is not implemented yet", nodeKey)
	}
}

func firstDSLSubSection(raw map[string]any) (string, map[string]any, bool) {
	if raw == nil {
		return "", nil, false
	}
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" || strings.HasPrefix(k, "_") {
			continue
		}
		if !strings.HasPrefix(k, "#") {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok || m == nil {
			return "", nil, false
		}
		return k, m, true
	}
	return "", nil, false
}

func (l *ExecutionLoop) evalDSLSubSection(st *dslExecState, name string, raw map[string]any) (circulation.Response, error) {
	resolution, ok := raw["resolution"].(map[string]any)
	if !ok || resolution == nil {
		// A sub-section with no resolution produces an empty OK response.
		return circulation.Response{
			IntentionID: st.in.IntentionID,
			Status:      circulation.ValueStatusOK,
			Payload:     map[string]any{},
		}, nil
	}
	return l.evalDSLNode(st, resolution)
}

func evalDSLOutputNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	payload := make(map[string]any, len(node))
	for k, v := range node {
		resolved, err := resolveDSLValue(st, v)
		if err != nil {
			return circulation.Response{}, fmt.Errorf("dsl output %s: %w", k, err)
		}
		payload[k] = resolved
	}
	return circulation.Response{
		IntentionID: st.in.IntentionID,
		Status:      circulation.ValueStatusOK,
		Payload:     payload,
	}, nil
}

func (l *ExecutionLoop) evalDSLInvokeNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	capRef, err := resolveDSLCapacityRef(st, node["@capacity"])
	if err != nil {
		return circulation.Response{}, err
	}
	toType := anyString(node["to_type"])
	if toType == "" {
		toType = circulation.ValueTypeUser
	}
	addr, err := parseDSLAddressRef(st.in.To.Context, capRef, toType)
	if err != nil {
		return circulation.Response{}, err
	}

	await := true
	if raw, ok := node["await_response"].(bool); ok {
		await = raw
	}

	params, matters, elemRefs, err := l.resolveDSLInvokeInput(st, node["with"])
	if err != nil {
		return circulation.Response{}, err
	}

	sub := circulation.Intention{
		IntentionID:   circulation.NewIntentionID(),
		AwaitResponse: await,
		To:            addr,
		From: circulation.Address{
			Context: st.in.To.Context,
			Type:    circulation.ValueTypeExecution,
			Cap:     st.in.To.Cap,
		},
		Params:      params,
		Matters:     matters,
		ElementRefs: elemRefs,
		Correlation: &circulation.Correlation{
			RootIntentionID:   dslRootIntentionID(st.in),
			ParentIntentionID: st.in.IntentionID,
		},
	}

	var resp circulation.Response
	if await {
		resp, err = l.evalDSLInvokeWithPolicy(node, sub)
		if err != nil {
			return circulation.Response{}, err
		}
	} else {
		if err := l.invokeNoWait(sub); err != nil {
			return circulation.Response{}, err
		}
		resp = circulation.Response{
			IntentionID: sub.IntentionID,
			To:          sub.From,
			From:        sub.To,
			Status:      circulation.ValueStatusOK,
			Payload:     map[string]any{},
		}
	}

	if as := anyString(node["as"]); as != "" && await {
		st.setAlias(as, resp)
	}

	if await && resp.Status == circulation.ValueStatusError {
		onError := anyString(node["on_error"])
		switch onError {
		case "continue", "collect":
			return circulation.Response{
				IntentionID: st.in.IntentionID,
				Status:      circulation.ValueStatusOK,
				Payload:     map[string]any{"collected_error": resp.Error},
			}, nil
		case "fail", "fail_fast", "":
			// default: propagate the error response
		}
	}

	return resp, nil
}

func (l *ExecutionLoop) evalDSLInvokeWithPolicy(node map[string]any, sub circulation.Intention) (circulation.Response, error) {
	timeout := parseDSLTimeout(node["timeout"])
	retryMax, retryDelay := parseDSLRetry(node["retry"])
	attempts := retryMax + 1
	if attempts < 1 {
		attempts = 1
	}

	var last circulation.Response
	for attempt := 0; attempt < attempts; attempt++ {
		resp, err := l.invokeAndAwaitWithTimeout(sub, timeout)
		if err != nil {
			return circulation.Response{}, err
		}
		last = resp
		if resp.Status == circulation.ValueStatusOK {
			return resp, nil
		}
		if attempt+1 < attempts && retryDelay > 0 {
			timer := time.NewTimer(retryDelay)
			select {
			case <-l.done:
				timer.Stop()
				return last, nil
			case <-timer.C:
			}
		}
	}
	return last, nil
}

func (l *ExecutionLoop) evalDSLSequenceNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	rawItems, ok := node["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return circulation.Response{}, fmt.Errorf("dsl >sequence requires non-empty items")
	}
	var last circulation.Response
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			return circulation.Response{}, fmt.Errorf("dsl >sequence item must be an object")
		}
		resp, err := l.evalDSLNode(st, item)
		if err != nil {
			return circulation.Response{}, err
		}
		last = resp
		if resp.Status == circulation.ValueStatusError {
			return last, nil
		}
	}
	return last, nil
}

func (l *ExecutionLoop) evalDSLIfNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	when, ok := node["when"].(map[string]any)
	if !ok || when == nil {
		return circulation.Response{}, fmt.Errorf("dsl >if requires when")
	}
	matched, err := evalDSLCondition(st, when)
	if err != nil {
		return circulation.Response{}, err
	}
	if matched {
		thenNode, ok := node[">then"].(map[string]any)
		if !ok || thenNode == nil {
			return circulation.Response{}, fmt.Errorf("dsl >if requires >then")
		}
		return l.evalDSLNode(st, thenNode)
	}
	elseNode, ok := node[">else"].(map[string]any)
	if !ok || elseNode == nil {
		return circulation.Response{
			IntentionID: st.in.IntentionID,
			Status:      circulation.ValueStatusOK,
			Payload:     map[string]any{},
		}, nil
	}
	return l.evalDSLNode(st, elseNode)
}

func (l *ExecutionLoop) evalDSLSwitchNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	onVal, err := resolveDSLSwitchOnValue(st, node["on"])
	if err != nil {
		return circulation.Response{}, err
	}
	rawCases, ok := node["cases"].([]any)
	if !ok {
		rawCases = nil
	}
	for _, rawCase := range rawCases {
		caseMap, ok := rawCase.(map[string]any)
		if !ok || caseMap == nil {
			return circulation.Response{}, fmt.Errorf("dsl >switch case must be an object")
		}
		caseVal, err := resolveDSLScalar(st, caseMap["value"])
		if err != nil {
			return circulation.Response{}, err
		}
		if dslValuesEqual(onVal, caseVal) {
			// spec uses "then" (no > prefix); accept both for robustness
			thenNode, ok := caseMap["then"].(map[string]any)
			if !ok || thenNode == nil {
				thenNode, ok = caseMap[">then"].(map[string]any)
			}
			if !ok || thenNode == nil {
				return circulation.Response{}, fmt.Errorf("dsl >switch case requires then")
			}
			return l.evalDSLNode(st, thenNode)
		}
	}
	defaultNode, ok := node[">default"].(map[string]any)
	if !ok || defaultNode == nil {
		return circulation.Response{
			IntentionID: st.in.IntentionID,
			Status:      circulation.ValueStatusOK,
			Payload:     map[string]any{},
		}, nil
	}
	return l.evalDSLNode(st, defaultNode)
}

func (l *ExecutionLoop) evalDSLParallelNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	rawItems, ok := node["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return circulation.Response{}, fmt.Errorf("dsl >parallel requires non-empty items")
	}

	limit := parseDSLLimit(node["limit"], len(rawItems))
	type branchResult struct {
		idx     int
		resp    circulation.Response
		err     error
		aliases map[string]circulation.Response
	}

	results := make([]branchResult, len(rawItems))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			return circulation.Response{}, fmt.Errorf("dsl >parallel item must be an object")
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, item map[string]any) {
			defer wg.Done()
			defer func() { <-sem }()

			child := cloneDSLExecState(st)
			resp, err := l.evalDSLNode(child, item)
			results[idx] = branchResult{
				idx:     idx,
				resp:    resp,
				err:     err,
				aliases: cloneAliases(child.aliases),
			}
		}(i, item)
	}
	wg.Wait()

	agg := make([]any, 0, len(results))
	var firstErr error
	var firstFail *circulation.Response
	for _, res := range results {
		if res.err != nil && firstErr == nil {
			firstErr = res.err
		}
		if res.err == nil && res.resp.Status == circulation.ValueStatusError && firstFail == nil {
			cp := res.resp
			firstFail = &cp
		}
		if res.err == nil && res.resp.Status == circulation.ValueStatusOK {
			for k, v := range res.aliases {
				st.aliases[k] = v
			}
		}
		agg = append(agg, map[string]any{
			"status":  res.resp.Status,
			"payload": res.resp.Payload,
			"error":   res.resp.Error,
		})
	}
	if firstErr != nil {
		return circulation.Response{}, firstErr
	}
	if firstFail != nil {
		return *firstFail, nil
	}
	return circulation.Response{
		IntentionID: st.in.IntentionID,
		Status:      circulation.ValueStatusOK,
		Payload: map[string]any{
			"items": agg,
		},
	}, nil
}

// evalDSLRaceNode
//
// Functional role (Brique DSL):
// - >sequence:
//   - launch every item concurrently, each in its own cloned scope
//   - return the FIRST branch that completes with status "ok"; merge only that branch's aliases into the parent scope
//   - when every branch has completed without a success, fail with the first error observed
//
// Contract:
// - There is NO cancellation: losing branches keep running to completion, their responses are discarded.
//   Effects performed by losing branches still happen — a race is only safe between effect-free branches.
// - Winner selection is by completion order of successful branches, not by item order.
// - All branches always start concurrently (no limit field): a race wants all competitors running.
// - When no branch succeeds: a branch-level Go error (malformed plan) takes precedence, otherwise the
//   first error response received is returned.

func (l *ExecutionLoop) evalDSLRaceNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	rawItems, ok := node["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return circulation.Response{}, fmt.Errorf("dsl >race requires non-empty items")
	}

	items := make([]map[string]any, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			return circulation.Response{}, fmt.Errorf("dsl >race item must be an object")
		}
		items = append(items, item)
	}

	type branchResult struct {
		resp    circulation.Response
		err     error
		aliases map[string]circulation.Response
	}

	// Buffered so losing branches never block after the winner returned.
	resultCh := make(chan branchResult, len(items))
	for _, item := range items {
		go func(item map[string]any) {
			child := cloneDSLExecState(st)
			resp, err := l.evalDSLNode(child, item)
			resultCh <- branchResult{resp: resp, err: err, aliases: cloneAliases(child.aliases)}
		}(item)
	}

	var firstErr error
	var firstFail *circulation.Response
	for range items {
		res := <-resultCh
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		if res.resp.Status == circulation.ValueStatusOK {
			for k, v := range res.aliases {
				st.aliases[k] = v
			}
			return res.resp, nil
		}
		if firstFail == nil {
			cp := res.resp
			firstFail = &cp
		}
	}

	if firstErr != nil {
		return circulation.Response{}, firstErr
	}
	return *firstFail, nil
}

func (l *ExecutionLoop) evalDSLForEachNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	collection, err := resolveDSLScalar(st, node["collection"])
	if err != nil {
		return circulation.Response{}, err
	}
	items, ok := collection.([]any)
	if !ok {
		return circulation.Response{}, fmt.Errorf("dsl >for_each collection must resolve to an array")
	}
	doNode, ok := node[">do"].(map[string]any)
	if !ok || doNode == nil {
		return circulation.Response{}, fmt.Errorf("dsl >for_each requires >do")
	}

	limit := parseDSLLimit(node["limit"], len(items))
	onError := anyString(node["on_error"])
	if onError == "" {
		onError = "fail"
	}

	type iterResult struct {
		idx  int
		resp circulation.Response
		err  error
	}
	results := make([]iterResult, len(items))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, item any) {
			defer wg.Done()
			defer func() { <-sem }()

			child := cloneDSLExecState(st)
			child.pushScope(map[string]any{
				dslLoopItemAlias:  item,
				dslLoopIndexAlias: idx,
			})
			resp, err := l.evalDSLNode(child, doNode)
			child.popScope()
			results[idx] = iterResult{idx: idx, resp: resp, err: err}
		}(i, item)
	}
	wg.Wait()

	agg := make([]any, 0, len(items))
	for _, res := range results {
		if res.err != nil {
			if onError == "continue" {
				continue
			}
			return circulation.Response{}, res.err
		}
		if res.resp.Status == circulation.ValueStatusError {
			if onError == "continue" {
				continue
			}
			return res.resp, nil
		}
		agg = append(agg, res.resp.Payload)
	}
	return circulation.Response{
		IntentionID: st.in.IntentionID,
		Status:      circulation.ValueStatusOK,
		Payload: map[string]any{
			"items": agg,
		},
	}, nil
}

func (l *ExecutionLoop) evalDSLWhileNode(st *dslExecState, node map[string]any) (circulation.Response, error) {
	when, ok := node["when"].(map[string]any)
	if !ok || when == nil {
		return circulation.Response{}, fmt.Errorf("dsl >while requires when")
	}
	doNode, ok := node[">do"].(map[string]any)
	if !ok || doNode == nil {
		return circulation.Response{}, fmt.Errorf("dsl >while requires >do")
	}
	maxIterations, err := parseDSLMaxIterations(node["max_iterations"])
	if err != nil {
		return circulation.Response{}, err
	}
	onError := anyString(node["on_error"])
	if onError == "" {
		onError = "fail"
	}

	agg := make([]any, 0)
	iterations := 0
	for {
		st.pushScope(map[string]any{dslLoopIndexAlias: iterations})
		matched, err := evalDSLCondition(st, when)
		if err != nil {
			st.popScope()
			return circulation.Response{}, err
		}
		if !matched {
			st.popScope()
			break
		}
		if iterations >= maxIterations {
			st.popScope()
			return circulation.Response{}, fmt.Errorf("dsl >while exceeded max_iterations (%d) without when becoming false", maxIterations)
		}

		resp, err := l.evalDSLNode(st, doNode)
		st.popScope()
		iterations++

		if err != nil {
			if onError == "continue" {
				continue
			}
			return circulation.Response{}, err
		}
		if resp.Status == circulation.ValueStatusError {
			if onError == "continue" {
				continue
			}
			return resp, nil
		}
		agg = append(agg, resp.Payload)
	}

	return circulation.Response{
		IntentionID: st.in.IntentionID,
		Status:      circulation.ValueStatusOK,
		Payload: map[string]any{
			"items":      agg,
			"iterations": iterations,
		},
	}, nil
}

func parseDSLMaxIterations(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return int(v), nil
		}
	case int:
		if v > 0 {
			return v, nil
		}
	case json.Number:
		if i, err := v.Int64(); err == nil && i > 0 {
			return int(i), nil
		}
	}
	return 0, fmt.Errorf("dsl >while requires max_iterations as a positive integer")
}

func (l *ExecutionLoop) resolveDSLInvokeInput(st *dslExecState, raw any) (map[string]any, []circulation.MatterRef, []circulation.ElementRef, error) {
	if raw == nil {
		return map[string]any{}, nil, nil, nil
	}
	in, ok := raw.(map[string]any)
	if !ok || in == nil {
		return nil, nil, nil, fmt.Errorf("dsl invoke with must be an object")
	}
	params := make(map[string]any, len(in))
	var matters []circulation.MatterRef
	var elemRefs []circulation.ElementRef
	for k, v := range in {
		if k == "@matter" {
			m, err := resolveDSLMatterRefs(st, v)
			if err != nil {
				return nil, nil, nil, err
			}
			matters = append(matters, m...)
			continue
		}
		if k == "@schema" || k == "@structure" || k == "@document" {
			kind := strings.TrimPrefix(k, "@")
			refs, err := resolveDSLElementRefs(st, kind, v)
			if err != nil {
				return nil, nil, nil, err
			}
			elemRefs = append(elemRefs, refs...)
			continue
		}
		if k == "@capacity" {
			// @capacity in with block is a named capacity ref (not the invocation target)
			refs, err := resolveDSLElementRefs(st, "capacity", v)
			if err != nil {
				return nil, nil, nil, err
			}
			elemRefs = append(elemRefs, refs...)
			continue
		}
		resolved, err := resolveDSLValue(st, v)
		if err != nil {
			return nil, nil, nil, err
		}
		params[k] = resolved
	}
	return params, matters, elemRefs, nil
}

func resolveDSLElementRefs(st *dslExecState, kind string, raw any) ([]circulation.ElementRef, error) {
	switch v := raw.(type) {
	case string:
		// direct path or $ref: "@schema": "/root/..."  or "@structure": "$input.plan"
		resolved, err := resolveDSLValue(st, v)
		if err != nil {
			return nil, fmt.Errorf("dsl @%s: %w", kind, err)
		}
		ref := anyString(resolved)
		if ref == "" {
			return nil, fmt.Errorf("dsl @%s value must be a non-empty string", kind)
		}
		ctx, id, err := parseDSLElementRef(st.in.To.Context, ref)
		if err != nil {
			return nil, fmt.Errorf("dsl @%s: %w", kind, err)
		}
		return []circulation.ElementRef{{Context: ctx, ID: id, Kind: kind}}, nil
	case map[string]any:
		// named map: "@matter"-style { "repr_name": "/path" }
		var out []circulation.ElementRef
		for repr, rawRef := range v {
			resolved, err := resolveDSLValue(st, rawRef)
			if err != nil {
				return nil, fmt.Errorf("dsl @%s %s: %w", kind, repr, err)
			}
			ref := anyString(resolved)
			if ref == "" {
				return nil, fmt.Errorf("dsl @%s %s must resolve to a string ref", kind, repr)
			}
			ctx, id, err := parseDSLElementRef(st.in.To.Context, ref)
			if err != nil {
				return nil, fmt.Errorf("dsl @%s %s: %w", kind, repr, err)
			}
			out = append(out, circulation.ElementRef{Context: ctx, ID: id, Repr: repr, Kind: kind})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("dsl @%s must be a string path or named map, got %T", kind, raw)
	}
}

func resolveDSLCapacityRef(st *dslExecState, raw any) (string, error) {
	resolved, err := resolveDSLValue(st, raw)
	if err != nil {
		return "", fmt.Errorf("dsl @capacity: %w", err)
	}
	ref := anyString(resolved)
	if ref == "" {
		return "", fmt.Errorf("dsl invoke requires @capacity")
	}
	return ref, nil
}

func resolveDSLMatterRefs(st *dslExecState, raw any) ([]circulation.MatterRef, error) {
	src, ok := raw.(map[string]any)
	if !ok || src == nil {
		return nil, fmt.Errorf("dsl @matter must be an object")
	}
	var out []circulation.MatterRef
	for name, rawRef := range src {
		resolved, err := resolveDSLValue(st, rawRef)
		if err != nil {
			return nil, err
		}
		refStr, ok := resolved.(string)
		if !ok || strings.TrimSpace(refStr) == "" {
			return nil, fmt.Errorf("dsl @matter %s must resolve to a string ref", name)
		}
		ctx, id, err := parseDSLElementRef(st.in.To.Context, refStr)
		if err != nil {
			return nil, err
		}
		out = append(out, circulation.MatterRef{
			Context: ctx,
			ID:      id,
			Repr:    name,
		})
	}
	return out, nil
}

func resolveDSLValue(st *dslExecState, raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		if strings.HasPrefix(v, "$") {
			return st.resolveValueRef(v)
		}
		return v, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			r, err := resolveDSLValue(st, item)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			r, err := resolveDSLValue(st, item)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return raw, nil
	}
}

func resolveDSLScalar(st *dslExecState, raw any) (any, error) {
	return resolveDSLValue(st, raw)
}

func resolveDSLSwitchOnValue(st *dslExecState, raw any) (any, error) {
	v, err := resolveDSLScalar(st, raw)
	if err == nil {
		return v, nil
	}
	if isDSLMissingPathError(err) {
		return nil, nil
	}
	return nil, err
}

func isDSLMissingPathError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "missing segment") || strings.Contains(msg, "cannot descend into <nil>")
}

func evalDSLCondition(st *dslExecState, raw map[string]any) (bool, error) {
	if andRaw, ok := raw["and"].([]any); ok && len(andRaw) > 0 {
		for _, item := range andRaw {
			m, ok := item.(map[string]any)
			if !ok {
				return false, fmt.Errorf("dsl condition and item must be an object")
			}
			okv, err := evalDSLCondition(st, m)
			if err != nil || !okv {
				return okv, err
			}
		}
		return true, nil
	}
	if orRaw, ok := raw["or"].([]any); ok && len(orRaw) > 0 {
		for _, item := range orRaw {
			m, ok := item.(map[string]any)
			if !ok {
				return false, fmt.Errorf("dsl condition or item must be an object")
			}
			okv, err := evalDSLCondition(st, m)
			if err != nil {
				return false, err
			}
			if okv {
				return true, nil
			}
		}
		return false, nil
	}

	op := anyString(raw["op"])
	if op == "" {
		return false, fmt.Errorf("dsl condition op is required")
	}
	left, err := resolveDSLScalar(st, raw["left"])
	if err != nil {
		if op == "exists" {
			return false, nil
		}
		return false, err
	}
	right, err := resolveDSLScalar(st, raw["right"])
	if err != nil && op != "exists" {
		return false, err
	}

	switch op {
	case "eq":
		return dslValuesEqual(left, right), nil
	case "neq":
		return !dslValuesEqual(left, right), nil
	case "exists":
		return left != nil, nil
	case "gt", "gte", "lt", "lte":
		lf, lok := dslFloat(left)
		rf, rok := dslFloat(right)
		if !lok || !rok {
			return false, nil
		}
		switch op {
		case "gt":
			return lf > rf, nil
		case "gte":
			return lf >= rf, nil
		case "lt":
			return lf < rf, nil
		case "lte":
			return lf <= rf, nil
		}
	case "in":
		if arr, ok := right.([]any); ok {
			for _, item := range arr {
				if dslValuesEqual(left, item) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func dslValuesEqual(a any, b any) bool {
	if af, aok := dslFloat(a); aok {
		if bf, bok := dslFloat(b); bok {
			return math.Abs(af-bf) < 1e-9
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func dslFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func parseDSLAddressRef(base circulation.ContextID, ref string, toType string) (circulation.Address, error) {
	ctx, capName, err := parseDSLElementRef(base, ref)
	if err != nil {
		return circulation.Address{}, err
	}
	if strings.TrimSpace(toType) == "" {
		toType = circulation.ValueTypeUser
	}
	return circulation.Address{
		Context: ctx,
		Type:    toType,
		Cap:     capName,
	}, nil
}

func parseDSLElementRef(base circulation.ContextID, ref string) (circulation.ContextID, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty DSL ref")
	}
	if strings.HasPrefix(ref, "@wrapper_") {
		return "", "", fmt.Errorf("@wrapper transport refs are forbidden in DSL descriptors")
	}
	if strings.HasPrefix(ref, "./") {
		return "", "", fmt.Errorf("relative DSL refs (./) are not supported: Communication cannot resolve them")
	}
	return splitDSLAbsoluteRef(ref)
}

func splitDSLContextAndName(base circulation.ContextID, rel string) (circulation.ContextID, string, error) {
	rel = strings.Trim(strings.TrimSpace(rel), "/")
	if rel == "" {
		return "", "", fmt.Errorf("empty relative DSL ref")
	}
	parts := strings.Split(rel, "/")
	name := strings.TrimSpace(parts[len(parts)-1])
	if name == "" {
		return "", "", fmt.Errorf("relative DSL ref missing element name")
	}
	ctx := strings.TrimRight(string(base), "/")
	if len(parts) > 1 {
		ctx = ctx + "/" + strings.Join(parts[:len(parts)-1], "/")
	}
	if ctx == "" {
		ctx = "/"
	}
	return circulation.ContextID(ctx), name, nil
}

func splitDSLAbsoluteRef(ref string) (circulation.ContextID, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty absolute DSL ref")
	}
	if strings.HasPrefix(ref, "@ext_") || strings.HasPrefix(ref, "@ui_") {
		idx := strings.LastIndex(ref, "/")
		if idx <= 0 || idx == len(ref)-1 {
			return "", "", fmt.Errorf("invalid absolute DSL ref: %s", ref)
		}
		return circulation.ContextID(ref[:idx]), ref[idx+1:], nil
	}
	if !strings.HasPrefix(ref, "/") {
		return "", "", fmt.Errorf("absolute DSL refs must follow Communication-supported absolute forms")
	}
	idx := strings.LastIndex(ref, "/")
	if idx <= 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("invalid absolute DSL ref: %s", ref)
	}
	return circulation.ContextID(ref[:idx]), ref[idx+1:], nil
}

func rewriteDSLTerminalResponse(root circulation.Intention, child circulation.Response) circulation.Response {
	out := child
	out.IntentionID = root.IntentionID
	out.To = root.From
	out.From = root.To
	return out
}

func dslRootIntentionID(in circulation.Intention) string {
	if in.Correlation != nil && strings.TrimSpace(in.Correlation.RootIntentionID) != "" {
		return strings.TrimSpace(in.Correlation.RootIntentionID)
	}
	return in.IntentionID
}

func parseDSLLimit(raw any, fallback int) int {
	if fallback <= 0 {
		fallback = 1
	}
	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case json.Number:
		if i, err := v.Int64(); err == nil && i > 0 {
			return int(i)
		}
	}
	return fallback
}

func parseDSLTimeout(raw any) time.Duration {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return 0
	}
	ms := parseDSLInt(m["ms"])
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func parseDSLRetry(raw any) (int, time.Duration) {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return 0, 0
	}
	max := parseDSLInt(m["max"])
	delay := parseDSLInt(m["delay_ms"])
	if max < 0 {
		max = 0
	}
	if delay < 0 {
		delay = 0
	}
	return max, time.Duration(delay) * time.Millisecond
}

func parseDSLInt(raw any) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return 0
}
