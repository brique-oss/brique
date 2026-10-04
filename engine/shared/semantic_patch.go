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

package shared

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type SemanticNodeKind string

const (
	SemanticNodeObject SemanticNodeKind = "object"
	SemanticNodeArray  SemanticNodeKind = "array"
	SemanticNodeScalar SemanticNodeKind = "scalar"
)

// SemanticPatchNode lets the shared patch engine mutate both ordinary decoded
// JSON and order-preserving JSON trees without depending on either family.
type SemanticPatchNode interface {
	SemanticClone() SemanticPatchNode
	SemanticKind() SemanticNodeKind
	SemanticObjectGet(string) (SemanticPatchNode, bool)
	SemanticObjectSet(string, SemanticPatchNode)
	SemanticObjectDelete(string)
	SemanticObjectLen() int
	SemanticArrayLen() int
	SemanticArrayGet(int) (SemanticPatchNode, bool)
	SemanticArraySet(int, SemanticPatchNode) bool
	SemanticArrayInsert(int, SemanticPatchNode) bool
	SemanticArrayDelete(int) bool
	SemanticScalar() any
	SemanticNew(any) SemanticPatchNode
	SemanticAny() any
}

type SemanticPathSegment struct {
	Key   string
	Index int
	IsKey bool
}

func (s SemanticPathSegment) Any() any {
	if s.IsKey {
		return s.Key
	}
	return s.Index
}

type SemanticPatchOperation struct {
	Op    string
	Path  []SemanticPathSegment
	Value any
}

type SemanticPatch struct {
	Operations []SemanticPatchOperation
}

type SemanticPatchOptions struct {
	AllowedRoots   map[string]bool
	ProtectedRoots map[string]bool
	CreateObjects  bool
}

type SemanticPatchError struct {
	Code           string
	OperationIndex int
	Operation      string
	Path           []any
	SegmentIndex   int
	ExpectedKind   string
	ActualKind     string
	Message        string
}

func (e *SemanticPatchError) Error() string {
	if e == nil {
		return "semantic patch error"
	}
	return e.Message
}

func semanticPatchErr(code, message string) *SemanticPatchError {
	return &SemanticPatchError{Code: code, OperationIndex: -1, SegmentIndex: -1, Message: message}
}

func ParseSemanticPatch(raw any) (SemanticPatch, error) {
	var result SemanticPatch
	patch, ok := raw.(map[string]any)
	if !ok || patch == nil {
		return result, semanticPatchErr("invalid_patch", "semantic_patch must be an object")
	}

	_, hasOperations := patch["operations"]
	_, hasAdd := patch["add"]
	_, hasRemove := patch["remove"]
	for key := range patch {
		if key != "operations" && key != "add" && key != "remove" {
			return result, semanticPatchErr("unknown_patch_field", fmt.Sprintf("semantic_patch field %q is not supported", key))
		}
	}
	if hasOperations && (hasAdd || hasRemove) {
		return result, semanticPatchErr("mixed_patch_forms", "semantic_patch.operations cannot be combined with legacy add/remove fields")
	}

	if hasOperations {
		items, ok := patch["operations"].([]any)
		if !ok || len(items) == 0 {
			return result, semanticPatchErr("invalid_operations", "semantic_patch.operations must be a non-empty array")
		}
		for index, item := range items {
			op, err := parseCanonicalSemanticOperation(item, index)
			if err != nil {
				return result, err
			}
			result.Operations = append(result.Operations, op)
		}
		return result, nil
	}

	for _, legacy := range []struct {
		name string
		raw  any
	}{{"add", patch["add"]}, {"remove", patch["remove"]}} {
		if legacy.raw == nil {
			continue
		}
		items, ok := legacy.raw.([]any)
		if !ok {
			return result, semanticPatchErr("invalid_operations", fmt.Sprintf("semantic_patch.%s must be an array", legacy.name))
		}
		for index, item := range items {
			op, err := parseLegacySemanticOperation(legacy.name, item, index)
			if err != nil {
				return result, err
			}
			result.Operations = append(result.Operations, op)
		}
	}
	if len(result.Operations) == 0 {
		return result, semanticPatchErr("empty_patch", "semantic_patch must include operations or legacy add/remove operations")
	}
	return result, nil
}

func parseCanonicalSemanticOperation(raw any, index int) (SemanticPatchOperation, error) {
	var result SemanticPatchOperation
	item, ok := raw.(map[string]any)
	if !ok || item == nil {
		return result, operationParseError(index, "operation must be an object")
	}
	for key := range item {
		if key != "op" && key != "path" && key != "value" {
			return result, operationParseError(index, fmt.Sprintf("operation field %q is not supported", key))
		}
	}
	op, _ := item["op"].(string)
	op = strings.TrimSpace(strings.ToLower(op))
	switch op {
	case "set", "add", "remove", "delete", "insert", "test":
	default:
		return result, operationParseError(index, fmt.Sprintf("unsupported semantic patch operation %q", op))
	}
	path, err := parseSemanticPath(item["path"])
	if err != nil {
		patchErr := operationParseError(index, err.Error())
		patchErr.Code = "invalid_path"
		return result, patchErr
	}
	value, hasValue := item["value"]
	if op != "delete" && !hasValue {
		return result, operationParseError(index, fmt.Sprintf("operation %q requires value", op))
	}
	if op == "delete" && hasValue {
		return result, operationParseError(index, "operation \"delete\" must not include value")
	}
	return SemanticPatchOperation{Op: op, Path: path, Value: value}, nil
}

func parseLegacySemanticOperation(op string, raw any, index int) (SemanticPatchOperation, error) {
	item, ok := raw.(map[string]any)
	if !ok || item == nil {
		return SemanticPatchOperation{}, operationParseError(index, fmt.Sprintf("semantic_patch.%s operation must be an object", op))
	}
	for key := range item {
		if key != "path" && key != "value" {
			return SemanticPatchOperation{}, operationParseError(index, fmt.Sprintf("semantic_patch.%s field %q is not supported", op, key))
		}
	}
	path, err := parseSemanticPath(item["path"])
	if err != nil {
		patchErr := operationParseError(index, err.Error())
		patchErr.Code = "invalid_path"
		return SemanticPatchOperation{}, patchErr
	}
	value, ok := item["value"]
	if !ok {
		return SemanticPatchOperation{}, operationParseError(index, fmt.Sprintf("semantic_patch.%s operation requires value", op))
	}
	return SemanticPatchOperation{Op: op, Path: path, Value: value}, nil
}

func operationParseError(index int, message string) *SemanticPatchError {
	return &SemanticPatchError{Code: "invalid_operation", OperationIndex: index, SegmentIndex: -1, Message: message}
}

func parseSemanticPath(raw any) ([]SemanticPathSegment, error) {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return nil, fmt.Errorf("path must be a non-empty array of strings and non-negative integer indexes")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("path must be a non-empty array of strings and non-negative integer indexes")
	}
	path := make([]SemanticPathSegment, 0, len(items))
	for index, item := range items {
		switch value := item.(type) {
		case string:
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("path segment %d must not be empty", index)
			}
			path = append(path, SemanticPathSegment{Key: value, IsKey: true})
		default:
			number, valid := semanticPathIndex(value)
			if !valid {
				return nil, fmt.Errorf("path segment %d must be a string or non-negative integer", index)
			}
			path = append(path, SemanticPathSegment{Index: number})
		}
	}
	if !path[0].IsKey {
		return nil, fmt.Errorf("path root must be a string section name")
	}
	return path, nil
}

func semanticPathIndex(value any) (int, bool) {
	var parsed int64
	switch value := value.(type) {
	case int:
		parsed = int64(value)
	case int64:
		parsed = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value < 0 || value > float64(math.MaxInt) {
			return 0, false
		}
		parsed = int64(value)
	case json.Number:
		integer, err := strconv.ParseInt(value.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		parsed = integer
	default:
		return 0, false
	}
	if parsed < 0 || uint64(parsed) > uint64(math.MaxInt) {
		return 0, false
	}
	return int(parsed), true
}

func ApplySemanticPatch(root SemanticPatchNode, patch SemanticPatch, options SemanticPatchOptions) (SemanticPatchNode, error) {
	if root == nil || root.SemanticKind() != SemanticNodeObject {
		return nil, semanticPatchErr("invalid_document", "semantic patch document root must be an object")
	}
	for index, operation := range patch.Operations {
		if len(operation.Path) == 0 || !operation.Path[0].IsKey {
			return nil, applySemanticError(operation, index, 0, "invalid_path", "path root must be a string section name", "object_key", "index")
		}
		rootKey := operation.Path[0].Key
		if len(options.AllowedRoots) > 0 && !options.AllowedRoots[rootKey] {
			return nil, applySemanticError(operation, index, 0, "unsupported_root", fmt.Sprintf("path root %q is not allowed", rootKey), "allowed_root", rootKey)
		}
		if operation.Op == "delete" && len(operation.Path) == 1 && options.ProtectedRoots[rootKey] {
			return nil, applySemanticError(operation, index, 0, "protected_root", fmt.Sprintf("root section %q cannot be deleted", rootKey), "protected_object", "delete")
		}
		if operation.Op == "set" && len(operation.Path) == 1 && options.ProtectedRoots[rootKey] {
			kind := root.SemanticNew(operation.Value).SemanticKind()
			if kind != SemanticNodeObject {
				return nil, applySemanticError(operation, index, 0, "protected_root", fmt.Sprintf("root section %q must remain an object", rootKey), "object", string(kind))
			}
		}
	}

	working := root.SemanticClone()
	for index, operation := range patch.Operations {
		if err := applyOneSemanticOperation(working, operation, index, options); err != nil {
			return nil, err
		}
	}
	return working, nil
}

type semanticAncestor struct {
	node    SemanticPatchNode
	segment SemanticPathSegment
}

func applyOneSemanticOperation(root SemanticPatchNode, operation SemanticPatchOperation, operationIndex int, options SemanticPatchOptions) error {
	create := options.CreateObjects && (operation.Op == "set" || operation.Op == "add")
	parent, ancestors, err := resolveSemanticParent(root, operation, operationIndex, create)
	if err != nil {
		return err
	}
	leaf := operation.Path[len(operation.Path)-1]

	switch operation.Op {
	case "set":
		return semanticSet(parent, leaf, operation.Value, operation, operationIndex)
	case "add":
		return semanticAdd(parent, leaf, operation.Value, operation, operationIndex)
	case "remove":
		removed, err := semanticRemove(parent, leaf, operation.Value, operation, operationIndex)
		if err == nil && removed {
			pruneSemanticAncestors(ancestors, options.ProtectedRoots)
		}
		return err
	case "delete":
		deleted, err := semanticDelete(parent, leaf, operation, operationIndex)
		if err == nil && deleted {
			pruneSemanticAncestors(ancestors, options.ProtectedRoots)
		}
		return err
	case "insert":
		if leaf.IsKey || parent.SemanticKind() != SemanticNodeArray {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "insert path must end with an array index", "array_index", string(parent.SemanticKind()))
		}
		if !parent.SemanticArrayInsert(leaf.Index, parent.SemanticNew(operation.Value)) {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "index_out_of_bounds", fmt.Sprintf("array insertion index %d is out of bounds", leaf.Index), "0..len", strconv.Itoa(leaf.Index))
		}
		return nil
	case "test":
		node, found, err := semanticGet(parent, leaf, operation, operationIndex)
		if err != nil {
			return err
		}
		if !found || !SemanticValuesEqual(node.SemanticAny(), operation.Value) {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "test_failed", "semantic patch test operation failed", fmt.Sprint(operation.Value), semanticActualValue(node, found))
		}
		return nil
	default:
		return applySemanticError(operation, operationIndex, -1, "invalid_operation", fmt.Sprintf("unsupported operation %q", operation.Op), "supported_operation", operation.Op)
	}
}

func resolveSemanticParent(root SemanticPatchNode, operation SemanticPatchOperation, operationIndex int, create bool) (SemanticPatchNode, []semanticAncestor, error) {
	current := root
	ancestors := make([]semanticAncestor, 0, len(operation.Path)-1)
	for segmentIndex := 0; segmentIndex < len(operation.Path)-1; segmentIndex++ {
		segment := operation.Path[segmentIndex]
		nextSegment := operation.Path[segmentIndex+1]
		switch current.SemanticKind() {
		case SemanticNodeObject:
			if !segment.IsKey {
				return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "type_mismatch", "object traversal requires a string key", "object_key", "array_index")
			}
			next, found := current.SemanticObjectGet(segment.Key)
			if !found || next == nil {
				if !create {
					return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "path_not_found", fmt.Sprintf("path segment %q does not exist", segment.Key), "existing_value", "missing")
				}
				if !nextSegment.IsKey {
					return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "missing_array", fmt.Sprintf("cannot infer missing array at path segment %q", segment.Key), "existing_array", "missing")
				}
				next = current.SemanticNew(map[string]any{})
				current.SemanticObjectSet(segment.Key, next)
			}
			ancestors = append(ancestors, semanticAncestor{node: current, segment: segment})
			current = next
		case SemanticNodeArray:
			if segment.IsKey {
				return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "type_mismatch", "array traversal requires an integer index", "array_index", "object_key")
			}
			next, found := current.SemanticArrayGet(segment.Index)
			if !found {
				return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "index_out_of_bounds", fmt.Sprintf("array index %d is out of bounds", segment.Index), "existing_index", strconv.Itoa(segment.Index))
			}
			ancestors = append(ancestors, semanticAncestor{node: current, segment: segment})
			current = next
		default:
			return nil, nil, applySemanticError(operation, operationIndex, segmentIndex, "type_mismatch", "cannot traverse through a scalar value", "object_or_array", string(current.SemanticKind()))
		}
	}
	return current, ancestors, nil
}

func semanticSet(parent SemanticPatchNode, leaf SemanticPathSegment, value any, operation SemanticPatchOperation, operationIndex int) error {
	node := parent.SemanticNew(value)
	switch parent.SemanticKind() {
	case SemanticNodeObject:
		if !leaf.IsKey {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "object assignment requires a string key", "object_key", "array_index")
		}
		parent.SemanticObjectSet(leaf.Key, node)
		return nil
	case SemanticNodeArray:
		if leaf.IsKey {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "array assignment requires an integer index", "array_index", "object_key")
		}
		if !parent.SemanticArraySet(leaf.Index, node) {
			return applySemanticError(operation, operationIndex, len(operation.Path)-1, "index_out_of_bounds", fmt.Sprintf("array index %d is out of bounds", leaf.Index), "existing_index", strconv.Itoa(leaf.Index))
		}
		return nil
	default:
		return applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "cannot assign through scalar parent", "object_or_array", string(parent.SemanticKind()))
	}
}

func semanticAdd(parent SemanticPatchNode, leaf SemanticPathSegment, value any, operation SemanticPatchOperation, operationIndex int) error {
	existing, found, err := semanticGet(parent, leaf, operation, operationIndex)
	if err != nil {
		return err
	}
	if !found {
		return semanticSet(parent, leaf, []any{value}, operation, operationIndex)
	}
	switch existing.SemanticKind() {
	case SemanticNodeObject:
		return applySemanticError(operation, operationIndex, len(operation.Path)-1, "non_terminal", "add target is an object, not a terminal value", "scalar_or_array", "object")
	case SemanticNodeArray:
		for index := 0; index < existing.SemanticArrayLen(); index++ {
			item, _ := existing.SemanticArrayGet(index)
			if SemanticValuesEqual(item.SemanticAny(), value) {
				return nil
			}
		}
		existing.SemanticArrayInsert(existing.SemanticArrayLen(), existing.SemanticNew(value))
		return nil
	default:
		items := []any{existing.SemanticAny()}
		if !SemanticValuesEqual(existing.SemanticAny(), value) {
			items = append(items, value)
		}
		return semanticSet(parent, leaf, items, operation, operationIndex)
	}
}

func semanticRemove(parent SemanticPatchNode, leaf SemanticPathSegment, value any, operation SemanticPatchOperation, operationIndex int) (bool, error) {
	existing, found, err := semanticGet(parent, leaf, operation, operationIndex)
	if err != nil || !found {
		return false, err
	}
	switch existing.SemanticKind() {
	case SemanticNodeObject:
		return false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "non_terminal", "remove target is an object, not a terminal value", "scalar_or_array", "object")
	case SemanticNodeArray:
		for index := 0; index < existing.SemanticArrayLen(); index++ {
			item, _ := existing.SemanticArrayGet(index)
			if SemanticValuesEqual(item.SemanticAny(), value) {
				existing.SemanticArrayDelete(index)
				if existing.SemanticArrayLen() == 0 {
					deleted, deleteErr := semanticDelete(parent, leaf, operation, operationIndex)
					return deleted, deleteErr
				}
				return false, nil
			}
		}
		return false, nil
	default:
		if !SemanticValuesEqual(existing.SemanticAny(), value) {
			return false, nil
		}
		return semanticDelete(parent, leaf, operation, operationIndex)
	}
}

func semanticDelete(parent SemanticPatchNode, leaf SemanticPathSegment, operation SemanticPatchOperation, operationIndex int) (bool, error) {
	switch parent.SemanticKind() {
	case SemanticNodeObject:
		if !leaf.IsKey {
			return false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "object deletion requires a string key", "object_key", "array_index")
		}
		_, found := parent.SemanticObjectGet(leaf.Key)
		if found {
			parent.SemanticObjectDelete(leaf.Key)
		}
		return found, nil
	case SemanticNodeArray:
		if leaf.IsKey {
			return false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "array deletion requires an integer index", "array_index", "object_key")
		}
		if !parent.SemanticArrayDelete(leaf.Index) {
			return false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "index_out_of_bounds", fmt.Sprintf("array index %d is out of bounds", leaf.Index), "existing_index", strconv.Itoa(leaf.Index))
		}
		return true, nil
	default:
		return false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "cannot delete through scalar parent", "object_or_array", string(parent.SemanticKind()))
	}
}

func semanticGet(parent SemanticPatchNode, leaf SemanticPathSegment, operation SemanticPatchOperation, operationIndex int) (SemanticPatchNode, bool, error) {
	switch parent.SemanticKind() {
	case SemanticNodeObject:
		if !leaf.IsKey {
			return nil, false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "object lookup requires a string key", "object_key", "array_index")
		}
		node, found := parent.SemanticObjectGet(leaf.Key)
		return node, found, nil
	case SemanticNodeArray:
		if leaf.IsKey {
			return nil, false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "array lookup requires an integer index", "array_index", "object_key")
		}
		node, found := parent.SemanticArrayGet(leaf.Index)
		return node, found, nil
	default:
		return nil, false, applySemanticError(operation, operationIndex, len(operation.Path)-1, "type_mismatch", "cannot look up through scalar parent", "object_or_array", string(parent.SemanticKind()))
	}
}

func pruneSemanticAncestors(ancestors []semanticAncestor, protected map[string]bool) {
	for index := len(ancestors) - 1; index >= 0; index-- {
		ancestor := ancestors[index]
		child, found, _ := semanticGetForPrune(ancestor.node, ancestor.segment)
		if !found || child == nil || child.SemanticKind() != SemanticNodeObject || child.SemanticObjectLen() != 0 {
			return
		}
		if index == 0 && ancestor.segment.IsKey && protected[ancestor.segment.Key] {
			return
		}
		if ancestor.node.SemanticKind() == SemanticNodeObject && ancestor.segment.IsKey {
			ancestor.node.SemanticObjectDelete(ancestor.segment.Key)
		} else if ancestor.node.SemanticKind() == SemanticNodeArray && !ancestor.segment.IsKey {
			ancestor.node.SemanticArrayDelete(ancestor.segment.Index)
		} else {
			return
		}
	}
}

func semanticGetForPrune(parent SemanticPatchNode, segment SemanticPathSegment) (SemanticPatchNode, bool, error) {
	if parent.SemanticKind() == SemanticNodeObject && segment.IsKey {
		node, found := parent.SemanticObjectGet(segment.Key)
		return node, found, nil
	}
	if parent.SemanticKind() == SemanticNodeArray && !segment.IsKey {
		node, found := parent.SemanticArrayGet(segment.Index)
		return node, found, nil
	}
	return nil, false, fmt.Errorf("container mismatch")
}

func applySemanticError(operation SemanticPatchOperation, operationIndex, segmentIndex int, code, message, expected, actual string) *SemanticPatchError {
	path := make([]any, 0, len(operation.Path))
	for _, segment := range operation.Path {
		path = append(path, segment.Any())
	}
	return &SemanticPatchError{
		Code:           code,
		OperationIndex: operationIndex,
		Operation:      operation.Op,
		Path:           path,
		SegmentIndex:   segmentIndex,
		ExpectedKind:   expected,
		ActualKind:     actual,
		Message:        message,
	}
}

func semanticActualValue(node SemanticPatchNode, found bool) string {
	if !found || node == nil {
		return "missing"
	}
	return fmt.Sprint(node.SemanticAny())
}

func SemanticValuesEqual(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr == nil && berr == nil && string(ab) == string(bb) {
		return true
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}
