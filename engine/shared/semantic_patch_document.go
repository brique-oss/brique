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

type semanticAnyNode struct {
	kind   SemanticNodeKind
	object map[string]*semanticAnyNode
	array  []*semanticAnyNode
	scalar any
}

func NewSemanticPatchDocument(value any) SemanticPatchNode {
	return newSemanticAnyNode(value)
}

func newSemanticAnyNode(value any) *semanticAnyNode {
	switch value := value.(type) {
	case map[string]any:
		node := &semanticAnyNode{kind: SemanticNodeObject, object: make(map[string]*semanticAnyNode, len(value))}
		for key, child := range value {
			node.object[key] = newSemanticAnyNode(child)
		}
		return node
	case []any:
		node := &semanticAnyNode{kind: SemanticNodeArray, array: make([]*semanticAnyNode, 0, len(value))}
		for _, child := range value {
			node.array = append(node.array, newSemanticAnyNode(child))
		}
		return node
	default:
		return &semanticAnyNode{kind: SemanticNodeScalar, scalar: value}
	}
}

func (n *semanticAnyNode) SemanticClone() SemanticPatchNode {
	return newSemanticAnyNode(n.SemanticAny())
}

func (n *semanticAnyNode) SemanticKind() SemanticNodeKind {
	if n == nil {
		return SemanticNodeScalar
	}
	return n.kind
}

func (n *semanticAnyNode) SemanticObjectGet(key string) (SemanticPatchNode, bool) {
	if n == nil || n.kind != SemanticNodeObject {
		return nil, false
	}
	child, found := n.object[key]
	return child, found
}

func (n *semanticAnyNode) SemanticObjectSet(key string, value SemanticPatchNode) {
	if n == nil || n.kind != SemanticNodeObject {
		return
	}
	n.object[key] = semanticAnyFromNode(value)
}

func (n *semanticAnyNode) SemanticObjectDelete(key string) {
	if n != nil && n.kind == SemanticNodeObject {
		delete(n.object, key)
	}
}

func (n *semanticAnyNode) SemanticObjectLen() int {
	if n == nil || n.kind != SemanticNodeObject {
		return 0
	}
	return len(n.object)
}

func (n *semanticAnyNode) SemanticArrayLen() int {
	if n == nil || n.kind != SemanticNodeArray {
		return 0
	}
	return len(n.array)
}

func (n *semanticAnyNode) SemanticArrayGet(index int) (SemanticPatchNode, bool) {
	if n == nil || n.kind != SemanticNodeArray || index < 0 || index >= len(n.array) {
		return nil, false
	}
	return n.array[index], true
}

func (n *semanticAnyNode) SemanticArraySet(index int, value SemanticPatchNode) bool {
	if n == nil || n.kind != SemanticNodeArray || index < 0 || index >= len(n.array) {
		return false
	}
	n.array[index] = semanticAnyFromNode(value)
	return true
}

func (n *semanticAnyNode) SemanticArrayInsert(index int, value SemanticPatchNode) bool {
	if n == nil || n.kind != SemanticNodeArray || index < 0 || index > len(n.array) {
		return false
	}
	n.array = append(n.array, nil)
	copy(n.array[index+1:], n.array[index:])
	n.array[index] = semanticAnyFromNode(value)
	return true
}

func (n *semanticAnyNode) SemanticArrayDelete(index int) bool {
	if n == nil || n.kind != SemanticNodeArray || index < 0 || index >= len(n.array) {
		return false
	}
	n.array = append(n.array[:index], n.array[index+1:]...)
	return true
}

func (n *semanticAnyNode) SemanticScalar() any {
	if n == nil || n.kind != SemanticNodeScalar {
		return nil
	}
	return n.scalar
}

func (n *semanticAnyNode) SemanticNew(value any) SemanticPatchNode {
	return newSemanticAnyNode(value)
}

func (n *semanticAnyNode) SemanticAny() any {
	if n == nil {
		return nil
	}
	switch n.kind {
	case SemanticNodeObject:
		out := make(map[string]any, len(n.object))
		for key, child := range n.object {
			out[key] = child.SemanticAny()
		}
		return out
	case SemanticNodeArray:
		out := make([]any, 0, len(n.array))
		for _, child := range n.array {
			out = append(out, child.SemanticAny())
		}
		return out
	default:
		return n.scalar
	}
}

func semanticAnyFromNode(value SemanticPatchNode) *semanticAnyNode {
	if typed, ok := value.(*semanticAnyNode); ok {
		return typed
	}
	return newSemanticAnyNode(value.SemanticAny())
}
