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

// reflexive/reflexive_read.go
//
// Navigation / Read capabilities (Reflexive).
//
// - This file only wires the capability surface + handler stubs.
// - No semantic interpretation.
// - No mutation.
// - Real implementations can be added later without changing the loop.
//
// Contract reminders:
// - Handlers are called from ReflexiveLoop job goroutine (inbox never blocks).
// - Responses are emitted via Comm through ReflexiveLoop helpers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

// anyToInt
//
// Functional role (Brique DSL):
// - >sequence:
//   - inspect the dynamic type of the input value
//   - convert supported numeric-like values to `int`
//   - return converted value and conversion success flag
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - v any.
//
// Outputs:
// - returns (int, bool).
//
// Contract:
// - Returns exactly one `(int, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `0`, `false` when conversion is not supported.

func anyToInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	case uint:
		return int(x), true
	case uint64:
		return int(x), true
	default:
		return 0, false
	}
}

// capReadStructure
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/read.structure.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capReadStructure(msg circulation.Message) {
	in := msg.Intention

	// -----------------------------
	// Params
	// -----------------------------
	scope := ""
	depth := 1
	maxPerPath := 50

	if in.Params != nil {
		if s, ok := in.Params[configuration.KeyScope].(string); ok {
			scope = strings.Trim(strings.TrimSpace(s), "/")
		}
		if v, ok := anyToInt(in.Params[configuration.KeyDepth]); ok {
			depth = v
		}
		if v, ok := anyToInt(in.Params[configuration.KeyMaxPerPath]); ok {
			maxPerPath = v
		}
	}
	if depth < 0 {
		depth = 0
	}
	if maxPerPath <= 0 {
		maxPerPath = 50
	}
	hierarchyOnly := depth == 0

	// -----------------------------
	// Types
	// -----------------------------
	type NavItem struct {
		Path          string    `json:"path"`
		Kind          string    `json:"kind"`
		Name          string    `json:"name,omitempty"`
		ChildrenTotal int       `json:"children_total"`
		ChildrenTrunc bool      `json:"children_truncated,omitempty"`
		Children      []NavItem `json:"children,omitempty"`
	}

	// -----------------------------
	// Helpers
	// -----------------------------
	pathJoin := func(parts ...string) string {
		var cleaned []string
		for _, p := range parts {
			p = strings.Trim(strings.TrimSpace(p), "/")
			if p == "" {
				continue
			}
			cleaned = append(cleaned, p)
		}
		return strings.Join(cleaned, "/")
	}

	readContextChildren := func(dir string) (ctxName string, children []string) {
		p := filepath.Join(dir, ContextDescriptorFilename)
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil
		}
		var root map[string]any
		if err := json.Unmarshal(b, &root); err != nil {
			return "", nil
		}
		syn, _ := root[circulation.KeyBrique].(map[string]any)
		if syn == nil {
			return "", nil
		}
		if v, ok := syn[configuration.KeyContextName].(string); ok {
			ctxName = v
		}
		if arr, ok := syn[configuration.KeyChildList].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					children = append(children, strings.TrimSpace(s))
				}
			}
		}
		sort.Strings(children) // tri alpha des siblings
		return ctxName, children
	}

	// Scan matter/<id>.matter.json files (disk is source of truth, flat layout)
	listMatterIDsFromDisk := func(dir string) (ids []string, total int) {
		root := filepath.Join(dir, circulation.ValueMatter)
		ents, err := os.ReadDir(root)
		if err != nil {
			// missing dir allowed => empty
			return nil, 0
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSpace(e.Name())
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(name), MatterDescriptorExt) {
				continue
			}
			id := strings.TrimSuffix(name, MatterDescriptorExt)
			if id == "" {
				continue
			}
			ids = append(ids, id)
		}
		sort.Strings(ids) // tri alpha
		total = len(ids)
		if total > maxPerPath {
			ids = ids[:maxPerPath]
		}
		return ids, total
	}

	// Scan structure/*.json files (disk is source of truth)
	listStructureIDsFromDisk := func(dir string) (ids []string, total int) {
		root := filepath.Join(dir, circulation.ValueStructure)
		ents, err := os.ReadDir(root)
		if err != nil {
			// missing dir allowed => empty
			return nil, 0
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSpace(e.Name())
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(name), StructureDescriptorExt) {
				continue
			}
			id := strings.TrimSuffix(name, StructureDescriptorExt)
			if id == "" {
				continue
			}
			ids = append(ids, id)
		}
		sort.Strings(ids) // tri alpha
		total = len(ids)
		if total > maxPerPath {
			ids = ids[:maxPerPath]
		}
		return ids, total
	}

	// buildContextNode: pré-ordre (node puis children)
	hierarchyVisited := make(map[string]struct{})
	var buildContextNode func(dir string, relPath string, depthCtx int) NavItem
	buildContextNode = func(dir string, relPath string, depthCtx int) NavItem {
		ctxName, childCtx := readContextChildren(dir)

		node := NavItem{
			Path:          relPath,
			Kind:          circulation.ValueContext,
			Name:          ctxName,
			ChildrenTotal: 0,
		}

		children := make([]NavItem, 0, 8)

		if hierarchyOnly {
			canonicalDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				canonicalDir = filepath.Clean(dir)
			}
			if _, seen := hierarchyVisited[canonicalDir]; seen {
				return node
			}
			hierarchyVisited[canonicalDir] = struct{}{}

			var validChildren []string
			for _, ch := range childCtx {
				chDir := filepath.Join(dir, ch)
				if st, err := os.Stat(chDir); err == nil && st.IsDir() {
					validChildren = append(validChildren, ch)
				}
			}

			total := len(validChildren)
			shown := validChildren
			if total > maxPerPath {
				shown = validChildren[:maxPerPath]
			}
			for _, ch := range shown {
				children = append(children, buildContextNode(
					filepath.Join(dir, ch),
					pathJoin(relPath, ch),
					0,
				))
			}
			node.ChildrenTotal = total
			node.ChildrenTrunc = total > len(children)
			node.Children = children
			return node
		}

		// context.json
		if _, err := os.Stat(filepath.Join(dir, ContextDescriptorFilename)); err == nil {
			children = append(children, NavItem{
				Path:          pathJoin(relPath, ContextDescriptorFilename),
				Kind:          circulation.ValueContextFile,
				Name:          ContextDescriptorFilename,
				ChildrenTotal: 0,
			})
		}

		// Capacities/*.json
		{
			capDir := filepath.Join(dir, circulation.ValueCapacity)
			if st, err := os.Stat(capDir); err == nil && st.IsDir() {
				ents, _ := os.ReadDir(capDir)
				var caps []string
				for _, e := range ents {
					if e.IsDir() {
						continue
					}
					n := e.Name()
					if n == "" || strings.HasPrefix(n, ".") {
						continue
					}
					if strings.HasSuffix(strings.ToLower(n), CapacityDescriptorExt) {
						caps = append(caps, n)
					}
				}
				sort.Strings(caps)
				total := len(caps)
				shown := caps
				if total > maxPerPath {
					shown = caps[:maxPerPath]
				}
				capNode := NavItem{
					Path:          pathJoin(relPath, circulation.ValueCapacity),
					Kind:          circulation.ValueDir,
					Name:          circulation.ValueCapacity,
					ChildrenTotal: total,
					ChildrenTrunc: total > len(shown),
				}
				for _, fn := range shown {
					capNode.Children = append(capNode.Children, NavItem{
						Path:          pathJoin(relPath, circulation.ValueCapacity, fn),
						Kind:          circulation.ValueCapacity,
						Name:          fn,
						ChildrenTotal: 0,
					})
				}
				children = append(children, capNode)
			}
		}

		// documents/*.json
		{
			docDir := filepath.Join(dir, circulation.ValueDocument)
			if st, err := os.Stat(docDir); err == nil && st.IsDir() {
				ents, _ := os.ReadDir(docDir)
				var docs []string
				for _, e := range ents {
					if e.IsDir() {
						continue
					}
					n := e.Name()
					if n == "" || strings.HasPrefix(n, ".") {
						continue
					}
					if strings.HasSuffix(strings.ToLower(n), DocumentDescriptorExt) {
						docs = append(docs, n)
					}
				}
				sort.Strings(docs)
				total := len(docs)
				shown := docs
				if total > maxPerPath {
					shown = docs[:maxPerPath]
				}
				docNode := NavItem{
					Path:          pathJoin(relPath, circulation.ValueDocument),
					Kind:          circulation.ValueDir,
					Name:          circulation.ValueDocument,
					ChildrenTotal: total,
					ChildrenTrunc: total > len(shown),
				}
				for _, fn := range shown {
					docNode.Children = append(docNode.Children, NavItem{
						Path:          pathJoin(relPath, circulation.ValueDocument, fn),
						Kind:          circulation.ValueDocument,
						Name:          fn,
						ChildrenTotal: 0,
					})
				}
				children = append(children, docNode)
			}
		}

		// schema/*.json
		{
			schemaDir := filepath.Join(dir, circulation.ValueSchema)
			if st, err := os.Stat(schemaDir); err == nil && st.IsDir() {
				ents, _ := os.ReadDir(schemaDir)
				var schemas []string
				for _, e := range ents {
					if e.IsDir() {
						continue
					}
					n := e.Name()
					if n == "" || strings.HasPrefix(n, ".") {
						continue
					}
					if strings.HasSuffix(strings.ToLower(n), SchemaDescriptorExt) {
						schemas = append(schemas, n)
					}
				}
				sort.Strings(schemas)
				total := len(schemas)
				shown := schemas
				if total > maxPerPath {
					shown = schemas[:maxPerPath]
				}
				sNode := NavItem{
					Path:          pathJoin(relPath, circulation.ValueSchema),
					Kind:          circulation.ValueDir,
					Name:          circulation.ValueSchema,
					ChildrenTotal: total,
					ChildrenTrunc: total > len(shown),
				}
				for _, fn := range shown {
					sNode.Children = append(sNode.Children, NavItem{
						Path:          pathJoin(relPath, circulation.ValueSchema, fn),
						Kind:          circulation.ValueSchema,
						Name:          fn,
						ChildrenTotal: 0,
					})
				}
				children = append(children, sNode)
			}
		}

		// matter (from disk) — only include if directory exists
		{
			matterDir := filepath.Join(dir, circulation.ValueMatter)
			if st, err := os.Stat(matterDir); err == nil && st.IsDir() {
				ids, total := listMatterIDsFromDisk(dir)
				mNode := NavItem{
					Path:          pathJoin(relPath, circulation.ValueMatter),
					Kind:          circulation.ValueDir,
					Name:          circulation.ValueMatter,
					ChildrenTotal: total,
					ChildrenTrunc: total > len(ids),
				}
				for _, id := range ids {
					mNode.Children = append(mNode.Children, NavItem{
						Path:          pathJoin(relPath, circulation.ValueMatter, id),
						Kind:          circulation.ValueMatterItem,
						Name:          id,
						ChildrenTotal: 0,
					})
				}
				children = append(children, mNode)
			}
		}

		// structure (from disk) — only include if directory exists
		{
			structureDir := filepath.Join(dir, circulation.ValueStructure)
			if st, err := os.Stat(structureDir); err == nil && st.IsDir() {
				ids, total := listStructureIDsFromDisk(dir)
				sNode := NavItem{
					Path:          pathJoin(relPath, circulation.ValueStructure),
					Kind:          circulation.ValueDir,
					Name:          circulation.ValueStructure,
					ChildrenTotal: total,
					ChildrenTrunc: total > len(ids),
				}
				for _, id := range ids {
					sNode.Children = append(sNode.Children, NavItem{
						Path:          pathJoin(relPath, circulation.ValueStructure, id),
						Kind:          circulation.ValueStructureItem,
						Name:          id,
						ChildrenTotal: 0,
					})
				}
				children = append(children, sNode)
			}
		}

		// code/ and ui/ directories — scanned recursively
		{
			var scanGenericDir func(absDir string, relBase string) NavItem
			scanGenericDir = func(absDir string, relBase string) NavItem {
				name := filepath.Base(absDir)
				node := NavItem{
					Path: relBase,
					Kind: circulation.ValueDir,
					Name: name,
				}
				ents, _ := os.ReadDir(absDir)
				for _, e := range ents {
					eName := e.Name()
					if eName == "" || strings.HasPrefix(eName, ".") {
						continue
					}
					childRel := pathJoin(relBase, eName)
					childAbs := filepath.Join(absDir, eName)
					if e.IsDir() {
						child := scanGenericDir(childAbs, childRel)
						node.Children = append(node.Children, child)
					} else {
						node.Children = append(node.Children, NavItem{
							Path: childRel,
							Kind: "file",
							Name: eName,
						})
					}
				}
				node.ChildrenTotal = len(node.Children)
				if node.ChildrenTotal > maxPerPath {
					node.ChildrenTrunc = true
					node.Children = node.Children[:maxPerPath]
				}
				return node
			}

			for _, dirName := range []string{"code", "ui"} {
				absDir := filepath.Join(dir, dirName)
				if st, err := os.Stat(absDir); err == nil && st.IsDir() {
					children = append(children, scanGenericDir(absDir, pathJoin(relPath, dirName)))
				}
			}
		}

		// Child contexts (recursive by depthCtx)
		if depthCtx > 0 && len(childCtx) > 0 {
			total := len(childCtx)
			shown := childCtx
			if total > maxPerPath {
				shown = childCtx[:maxPerPath]
			}
			for _, ch := range shown {
				chDir := filepath.Join(dir, ch)
				if st, err := os.Stat(chDir); err != nil || !st.IsDir() {
					continue
				}
				children = append(children, buildContextNode(chDir, pathJoin(relPath, ch), depthCtx-1))
			}
		}

		// Tri alpha des siblings (déterministe)
		sort.Slice(children, func(i, j int) bool { return children[i].Path < children[j].Path })

		// Gate max_per_path sur CE node (en plus des gates internes déjà appliquées)
		node.ChildrenTotal = len(children)
		if node.ChildrenTotal > maxPerPath {
			node.ChildrenTrunc = true
			node.Children = children[:maxPerPath]
		} else {
			node.Children = children
		}
		return node
	}

	// -----------------------------
	// Resolve scope (context-child only)
	// -----------------------------
	targetDir := l.frame.ContextDir
	rel := ""
	if scope != "" {
		// Invariant: scope is a SINGLE child context name (no path).
		if strings.Contains(scope, "/") || strings.Contains(scope, "\\") {
			l.emitResponseError(errorResp(in,
				circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: "invalid_scope"},
				"scope must be a single child context name (no path)",
			))
			return
		}

		s := strings.TrimSpace(scope)
		if s == "" || s == "." || s == ".." {
			l.emitResponseError(errorResp(in,
				circulation.ValueCodeInvalid,
				map[string]any{circulation.KeyReason: "invalid_scope"},
				"invalid scope name",
			))
			return
		}
		next := filepath.Join(l.frame.ContextDir, s)
		st, err := os.Stat(next)
		if err != nil || !st.IsDir() {
			l.emitResponseError(errorResp(in,
				circulation.ValueCodeNotFound,
				map[string]any{circulation.KeyReason: "scope_not_found"},
				"scope not found",
			))
			return
		}
		// Must be a context child: require context.json
		if _, err := os.Stat(filepath.Join(next, ContextDescriptorFilename)); err != nil {
			l.emitResponseError(errorResp(in,
				circulation.ValueCodeNotFound,
				map[string]any{circulation.KeyReason: "scope_not_a_context"},
				"scope is not a context",
			))
			return
		}
		targetDir = next
		rel = pathJoin(rel, s)
	}

	root := buildContextNode(targetDir, rel, depth)

	l.emitResponseOK(in, map[string]any{
		circulation.KeyRoot: root,
		circulation.KeyParams: map[string]any{
			circulation.KeyScope:      scope,
			circulation.KeyDepth:      depth,
			circulation.KeyMaxPerPath: maxPerPath,
		},
	})
}

// capReadMeaning
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/read.meaning.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capReadMeaning(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.input is required"))
		return
	}

	rawIn, ok := in.Params[circulation.KeyInput].([]any)
	if !ok || len(rawIn) == 0 {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams, "field": "input"},
			"params.input must be a non-empty array"))
		return
	}

	results := make([]map[string]any, 0, len(rawIn))
	for _, it := range rawIn {
		item, _ := it.(map[string]any)
		if item == nil {
			results = append(results, meaningItemErr("", "", "", "invalid_item", "input item must be an object"))
			continue
		}

		kind, _ := item[circulation.KeyElementKind].(string)
		name, _ := item[circulation.KeyElementName].(string)
		kind = strings.TrimSpace(strings.ToLower(kind))
		name = strings.TrimSpace(name)

		// sections (always include brique)
		secSet := map[string]struct{}{circulation.KeyBrique: {}}
		if arr, ok := item[circulation.KeySections].([]any); ok {
			for _, s := range arr {
				ss, ok := s.(string)
				if !ok {
					continue
				}
				ss = strings.TrimSpace(strings.ToLower(ss))
				switch ss {
				case "", circulation.ValueModeBrique:
					secSet[circulation.KeyBrique] = struct{}{}
				case circulation.ValueObjective:
					secSet[circulation.ValueObjective] = struct{}{}
				case circulation.ValueFunctional:
					secSet[circulation.ValueFunctional] = struct{}{}
				case circulation.ValueSubjective:
					secSet[circulation.ValueSubjective] = struct{}{}
				default:
					// ignore unknown section (permissive)
				}
			}
		}

		// include_resolution: when false (default), strip functional.#root.resolution for capacity items.
		includeResolution := false
		if v, ok := item[circulation.KeyIncludeResolution].(bool); ok {
			includeResolution = v
		}

		// detail: default "invoke" (capacity items only) reduces functional.#root to {role, inputs, outputs}; "full" returns everything.
		invokeDetail := true
		if v, ok := item[circulation.KeyDetailParam].(string); ok {
			invokeDetail = strings.TrimSpace(strings.ToLower(v)) != circulation.ValueFull
		}

		// resolve to disk path using constants
		diskPath, navPath, outKind, outName, rerr := resolveMeaningByKind(l.frame.ContextDir, kind, name)
		if rerr != nil {
			results = append(results, meaningItemErr(outKind, outName, navPathOrRaw(navPath, ""), "resolve_failed", rerr.Error()))
			continue
		}

		// scope enforcement
		okScope, serr := ensureWithinDir(l.frame.ContextDir, diskPath)
		if serr != nil || !okScope {
			msg := "scope violation"
			if serr != nil {
				msg = serr.Error()
			}
			results = append(results, meaningItemErr(outKind, outName, navPath, "scope_violation", msg))
			continue
		}

		// read + strict parse
		b, err := os.ReadFile(diskPath)
		if err != nil {
			code := circulation.ValueCodeNotFound
			if !os.IsNotExist(err) {
				code = circulation.ValueCodeReadFail
			}
			results = append(results, meaningItemErr(outKind, outName, navPath, code, err.Error()))
			continue
		}

		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
			msg := "invalid json"
			if err != nil {
				msg = err.Error()
			}
			results = append(results, meaningItemErr(outKind, outName, navPath, "invalid_json", msg))
			continue
		}

		orderedDoc, oerr := parseOrderedJSON(b)
		if oerr != nil {
			results = append(results, meaningItemErr(outKind, outName, navPath, "invalid_json", oerr.Error()))
			continue
		}
		filtered := orderedDoc.filteredObject(secSet)
		if kind == circulation.ValueCapacity {
			if funcNode, ok := filtered.get(circulation.ValueFunctional); ok {
				if rootNode, ok := funcNode.get(circulation.KeyDSLRoot); ok {
					if invokeDetail {
						funcNode.set(circulation.KeyDSLRoot, rootNode.filteredObject(map[string]struct{}{
							circulation.KeyRole:    {},
							circulation.KeyInputs:  {},
							circulation.KeyOutputs: {},
						}))
					} else if !includeResolution {
						rootNode.deleteKey(circulation.KeyResolution)
					}
				}
			}
		}
		filteredJSON, oerr := marshalOrderedJSON(filtered)
		if oerr != nil {
			results = append(results, meaningItemErr(outKind, outName, navPath, "invalid_json", oerr.Error()))
			continue
		}

		results = append(results, map[string]any{
			circulation.KeyOK:          true,
			circulation.KeyElementKind: outKind,
			circulation.KeyElementName: outName,
			circulation.KeyPath:        navPath,
			circulation.KeyDesc:        json.RawMessage(filteredJSON),
		})
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// meaningItemErr
//
// Functional role (Brique DSL):
// - >sequence:
//   - build one normalized failed read.meaning item object
//   - attach the item identity fields
//   - attach the nested error object
//   - return the item payload
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned map and nested error map.
//
// Inputs:
// - kind, name, path, code, message string.
//
// Outputs:
// - returns map[string]any.
//
// Contract:
// - Returns exactly one item-shaped payload with `ok=false`.
// - Emits no response, trace, or outbound message.

func meaningItemErr(kind, name, path, code, message string) map[string]any {
	return map[string]any{
		circulation.KeyOK:          false,
		circulation.KeyElementKind: kind,
		circulation.KeyElementName: name,
		circulation.KeyPath:        path,
		circulation.KeyError: map[string]any{
			circulation.KeyCode:    code,
			circulation.KeyMessage: message,
		},
	}
}

// resolveMeaningByKind
//
// Functional role (Brique DSL):
// - >sequence:
//   - normalize the requested output kind and name
//   - reject path-like element names for non-context kinds
//   - dispatch on requested element kind
//   - derive descriptor disk path and navigation path
//   - refuse kinds owned by MatterLoop
//   - return resolved paths or an error
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - ctxDir, kind, name string.
//
// Outputs:
// - returns (diskPath, navPath, outKind, outName string, err error).
//
// Contract:
// - Returns exactly one `(diskPath, navPath, outKind, outName, err)` tuple.
// - Emits no response, trace, or outbound message.
// - Refuses matter and structure meaning reads because they are owned by MatterLoop.

func resolveMeaningByKind(ctxDir, kind, name string) (diskPath, navPath, outKind, outName string, err error) {
	outKind = kind
	outName = name

	// Minimal safety: elementname must be an id, not a path.
	// (Scope enforcement is also applied later, but this avoids odd navPath and pointless IO.)
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		// allow empty name for context only (handled below)
		if kind != circulation.ValueContext {
			err = fmt.Errorf("invalid elementname")
			return
		}
	}

	switch kind {
	case circulation.ValueContext:
		// name can be "", ".", or ignored
		outName = ""
		navPath = ContextDescriptorFilename
		diskPath = filepath.Join(ctxDir, ContextDescriptorFilename)
		return

	case circulation.ValueCapacity:
		if name == "" {
			err = fmt.Errorf("capacity elementname is required")
			return
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueCapacity, name+CapacityDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueCapacity, name+CapacityDescriptorExt)
		return

	case circulation.ValueSchema:
		if name == "" {
			err = fmt.Errorf("schema elementname is required")
			return
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueSchema, name+SchemaDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueSchema, name+SchemaDescriptorExt)
		return

	case circulation.ValueDocument:
		if name == "" {
			err = fmt.Errorf("document elementname is required")
			return
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueDocument, name+DocumentDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueDocument, name+DocumentDescriptorExt)
		return

	case circulation.ValueStructure:
		// meaning read is owned by MatterLoop (Reflexive must not read structure descriptors directly).
		err = fmt.Errorf("structure is owned by matter loop")
		return

	case circulation.ValueMatter:
		// meaning read is owned by MatterLoop (Reflexive must not read structure descriptors directly).
		err = fmt.Errorf("matter is owned by matter loop")
		return

	default:
		err = fmt.Errorf("unsupported elementkind %q", kind)
		return
	}
}

// filterSections
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if document is nil or the requested section set is empty: return the original document
//   - sort requested section keys deterministically
//   - copy only matching top-level sections into a new map
//   - return the filtered document
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned filtered map when filtering is applied.
//
// Inputs:
// - doc map[string]any, secSet map[string]struct{}.
//
// Outputs:
// - returns map[string]any.
//
// Contract:
// - Returns exactly one `map[string]any`.
// - Emits no response, trace, or outbound message.
// - Returns the original `doc` unchanged when the section set is empty.

func filterSections(doc map[string]any, secSet map[string]struct{}) map[string]any {
	if doc == nil || len(secSet) == 0 {
		return doc
	}
	out := make(map[string]any, len(secSet))
	keys := make([]string, 0, len(secSet))
	for k := range secSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	return out
}

// navPathOrRaw
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim and test the normalized navigation path
//   - return the navigation path when present
//   - otherwise return the raw path
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - nav, raw string.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Returns `nav` when non-empty after trim; otherwise returns `raw`.

func navPathOrRaw(nav, raw string) string {
	if strings.TrimSpace(nav) != "" {
		return nav
	}
	return raw
}

// ensureWithinDir
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate non-empty root and target paths
//   - compute absolute root and target paths
//   - derive the relative path from root to target
//   - reject targets that escape the root directory
//   - return scope validation result
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - rootDir, targetPath string.
//
// Outputs:
// - returns (bool, error).
//
// Contract:
// - Returns exactly one `(bool, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports scope-validation failure via the returned `error`; callers convert it to circulation response semantics.

func ensureWithinDir(rootDir, targetPath string) (bool, error) {
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(targetPath) == "" {
		return false, fmt.Errorf("empty root or target")
	}
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return false, err
	}
	tAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(rootAbs, tAbs)
	if err != nil {
		return false, err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return true, nil
	}
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return false, fmt.Errorf("path escapes context root")
	}
	return true, nil
}

// anyToInt64
//
// Functional role (Brique DSL):
// - >sequence:
//   - inspect the dynamic type of the input value
//   - convert supported numeric-like values to `int64`
//   - reject unsupported values and oversized `uint64`
//   - return converted value and conversion success flag
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - v any.
//
// Outputs:
// - returns (int64, bool).
//
// Contract:
// - Returns exactly one `(int64, bool)` pair.
// - Emits no response, trace, or outbound message.
// - Returns `0`, `false` when conversion is not supported.

func anyToInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		// best-effort safe cast
		if x > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(x), true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	default:
		return 0, false
	}
}

// capReadDocument
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/read.document.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capReadDocument(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	// -----------------------------
	// 1) Refuse external instance callers for now
	// -----------------------------
	fromCtx := strings.TrimSpace(string(in.From.Context))
	if strings.HasPrefix(fromCtx, "@ext_") {
		l.emitResponseError(errorResp(
			in,
			circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason: "external_instance_not_supported",
				"from_context":        fromCtx,
			},
			"external instance callers are not supported yet",
		))
		return
	}

	// -----------------------------
	// 2) Parse params
	// -----------------------------
	type reqItem struct {
		Name string `json:"name"`
	}
	var items []reqItem

	if in.Params != nil {
		raw, ok := in.Params[circulation.KeyItems].([]any)
		if ok {
			items = make([]reqItem, 0, len(raw))
			for _, it := range raw {
				m, ok := it.(map[string]any)
				if !ok || m == nil {
					continue
				}
				name, _ := m[circulation.KeyName].(string)
				items = append(items, reqItem{Name: strings.TrimSpace(name)})
			}
		}
	}

	if len(items) == 0 {
		l.emitResponseError(errorResp(
			in,
			circulation.ValueCodeInvalid,
			map[string]any{
				circulation.KeyReason:  circulation.ValueReasonInvalidParams,
				circulation.KeyMissing: circulation.KeyItems,
			},
			"params.items is required",
		))
		return
	}

	// -----------------------------
	// 3) Resolve each document
	// -----------------------------
	type outErr struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	}
	type outTarget struct {
		Kind string `json:"kind"`          // "local" | "ref"
		Rel  string `json:"rel,omitempty"` // relative to context root (for local)
		Abs  string `json:"abs,omitempty"` // absolute fs path (for local)
		Ref  string `json:"ref,omitempty"` // external ref/uri (for ref)
	}
	type outItem struct {
		Ok     bool      `json:"ok"`
		Name   string    `json:"name"`
		Target outTarget `json:"target,omitempty"`
		Error  *outErr   `json:"error,omitempty"`
	}

	readJSONFile := func(abs string) (map[string]any, error) {
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		return doc, nil
	}

	getBrique := func(doc map[string]any) map[string]any {
		if doc == nil {
			return nil
		}
		m, _ := doc[circulation.KeyBrique].(map[string]any)
		return m
	}

	// extractDocTarget resolves doc by name:
	// - reads <ctxDir>/document/<name>.json
	// - if brique.ref|href|uri => Kind="ref"
	// - else if brique.file|content|content_path => local pointer
	// - else fallback to local convention: document/<name>
	extractDocTarget := func(reqName string) (outTarget, bool, string) {
		reqName = strings.TrimSpace(reqName)
		if reqName == "" {
			return outTarget{}, false, "missing_name"
		}

		// Descriptor location: <ctxDir>/document/<name>.json
		descAbs := filepath.Join(l.frame.ContextDir, circulation.ValueDocument, reqName+DocumentDescriptorExt)

		doc, err := readJSONFile(descAbs)
		if err != nil {
			if os.IsNotExist(err) {
				// Strict design: a Document is an element anchored by its descriptor.
				// No fallback to raw files under document/ when descriptor is missing.
				return outTarget{}, false, "descriptor_not_found"
			}
			return outTarget{}, false, "descriptor_read_error"
		}

		syn := getBrique(doc)

		// 1) External reference
		if syn != nil {
			if v, ok := syn[circulation.KeyRef].(string); ok && strings.TrimSpace(v) != "" {
				return outTarget{Kind: circulation.ValueRef, Ref: strings.TrimSpace(v)}, true, ""
			}
		}

		// 2) Local content pointer (or deterministic default)
		contentRel := ""
		if syn != nil {
			// prefer canonical key if you have one
			if v, ok := syn[circulation.KeyFile].(string); ok && strings.TrimSpace(v) != "" {
				contentRel = strings.TrimSpace(v)
			}
		}
		if contentRel == "" {
			return outTarget{}, false, "missing_content_pointer"
		}

		// Normalize to slash, clean, enforce non-absolute and non-parent escape.
		contentRel = strings.ReplaceAll(contentRel, "\\", "/")
		contentRel = path.Clean(contentRel)
		if contentRel == "." || contentRel == "" {
			return outTarget{}, false, "invalid_content_path"
		}
		if strings.HasPrefix(contentRel, "/") || contentRel == ".." || strings.HasPrefix(contentRel, "../") {
			return outTarget{}, false, "invalid_content_path"
		}

		// Enforce under document/ ; if caller gave "file.md", assume document/file.md
		if contentRel != circulation.ValueDocument && !strings.HasPrefix(contentRel, circulation.ValueDocument+"/") {
			contentRel = filepath.ToSlash(filepath.Join(circulation.ValueDocument, contentRel))
		}

		contentAbs := filepath.Join(l.frame.ContextDir, filepath.FromSlash(contentRel))
		if _, err := os.Stat(contentAbs); err != nil {
			if os.IsNotExist(err) {
				return outTarget{}, false, "content_not_found"
			}
			return outTarget{}, false, "content_stat_error"
		}

		return outTarget{Kind: circulation.ValueLocal, Rel: contentRel, Abs: contentAbs}, true, ""
	}

	results := make([]outItem, 0, len(items))
	for _, it := range items {
		reqName := it.Name

		tgt, ok, reason := extractDocTarget(reqName)
		if !ok {
			code := circulation.ValueCodeNotFound
			msg := "document not found"

			switch reason {
			case "missing_name":
				code = circulation.ValueCodeInvalid
				msg = "missing document name"
			case "missing_content_pointer":
				code = circulation.ValueCodeInvalid
				msg = "document descriptor missing brique.file or brique.ref"
			case "descriptor_not_found":
				code = circulation.ValueCodeNotFound
				msg = "document descriptor not found"
			case "descriptor_read_error", "content_stat_error":
				code = circulation.ValueCodeInternal
				msg = "document resolution failed"
			case "invalid_content_path":
				code = circulation.ValueCodeInvalid
				msg = "invalid document content path"
			case "content_not_found":
				code = circulation.ValueCodeNotFound
				msg = "document content not found"
			}

			results = append(results, outItem{
				Ok:   false,
				Name: reqName,
				Error: &outErr{
					Code:    code,
					Message: msg,
					Details: map[string]any{circulation.KeyReason: reason},
				},
			})
			continue
		}

		results = append(results, outItem{
			Ok:     true,
			Name:   reqName,
			Target: tgt,
		})
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyTotal:  len(results),
		circulation.KeyResult: results,
	})
}

// capReadState
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/read.state.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capReadState(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	// -----------------------------
	// 1) Parse include (optional)
	// -----------------------------
	// Default: all four sections.
	include := map[string]bool{
		circulation.KeyContext:  true,
		circulation.KeyTrace:    true,
		circulation.KeyWrappers: true,
		circulation.KeyFamilies: true,
	}

	if in.Params != nil {
		if raw, ok := in.Params[circulation.KeyInclude].([]any); ok {
			for k := range include {
				include[k] = false
			}
			for _, it := range raw {
				s, ok := it.(string)
				if !ok {
					continue
				}
				s = strings.TrimSpace(strings.ToLower(s))
				if _, known := include[s]; known {
					include[s] = true
				}
			}
		}
	}

	payload := map[string]any{}

	// -----------------------------
	// 2) Context identity (frame)
	// -----------------------------
	if include[circulation.KeyContext] {
		payload[circulation.KeyContext] = map[string]any{
			circulation.KeyContextId: safeStr(func() string {
				if l.frame != nil {
					return l.frame.CtxId
				}
				return ""
			}()),
			configuration.KeyContextName: safeStr(func() string {
				if l.frame != nil {
					return l.frame.CtxName
				}
				return ""
			}()),
			configuration.KeyCxtExtName: safeStr(func() string {
				if l.frame != nil {
					return l.frame.CtxExtName
				}
				return ""
			}()),
			configuration.KeyCtxVersion: safeStr(func() string {
				if l.frame != nil {
					return l.frame.CtxVersion
				}
				return ""
			}()),
			configuration.KeyEngineVersion: safeStr(func() string {
				if l.frame != nil {
					return l.frame.EngineVers
				}
				return ""
			}()),
			circulation.KeyContextDir: safeStr(func() string {
				if l.frame != nil {
					return l.frame.ContextDir
				}
				return ""
			}()),
		}
	}

	// -----------------------------
	// 3) Trace (frame)
	// -----------------------------
	if include[circulation.KeyTrace] {
		enabled := false
		level := configuration.ValueConfigTraceLevelNormal
		if l.frame != nil {
			enabled = l.frame.TraceEnabled
			if strings.TrimSpace(l.frame.TraceLevel) != "" {
				level = l.frame.TraceLevel
			}
		}
		payload[circulation.KeyTrace] = map[string]any{
			configuration.KeyTraceEnabled: enabled,
			configuration.KeyTraceLevel:   level,
		}
	}

	// -----------------------------
	// 4) Wrappers (frame.Wrappers)
	// -----------------------------
	if include[circulation.KeyWrappers] {
		payload[circulation.KeyWrappers] = snapshotWrappers(l.frame)
	}

	// -----------------------------
	// 5) Families (frame.Runtime)
	// -----------------------------
	if include[circulation.KeyFamilies] {
		if l.frame != nil && l.frame.Runtime != nil && l.frame.Runtime.ListFamilyState != nil {
			payload[circulation.KeyFamilies] = snapshotFamilyStates(l.frame.Runtime.ListFamilyState())
		} else {
			payload[circulation.KeyFamilies] = map[string]any{}
		}
	}

	l.emitResponseOK(in, payload)
}

// ctxDirOrEmpty
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if frame is nil: return empty string
//   - trim the context directory string
//   - return the normalized context directory
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - frame *junction.ContextRegistry.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Nil frame maps to the empty string.

func ctxDirOrEmpty(frame *junction.ContextRegistry) string {
	if frame == nil {
		return ""
	}
	return strings.TrimSpace(frame.ContextDir)
}

// snapshotFamilyStates
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if family map is nil: return empty snapshot
//   - collect and sort family names
//   - convert family states to string-keyed values
//   - return deterministic family snapshot
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned snapshot map.
//
// Inputs:
// - m map[shared.FamilyName]shared.FamilyState.
//
// Outputs:
// - returns map[string]any.
//
// Contract:
// - Returns exactly one `map[string]any`.
// - Emits no response, trace, or outbound message.
// - Output order is deterministic through sorted family names.

func snapshotFamilyStates(m map[shared.FamilyName]shared.FamilyState) map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sortStrings(keys)
	for _, k := range keys {
		out[k] = fmt.Sprintf("%v", m[shared.FamilyName(k)])
	}
	return out
}

// snapshotWrappers
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if frame or wrapper registry is nil: return empty slice
//   - collect non-empty wrapper names
//   - sort wrapper names deterministically
//   - read each wrapper state under lock
//   - append serializable wrapper snapshots
//   - return wrapper snapshot slice
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates the returned wrapper snapshot slice and item maps.
//
// Inputs:
// - frame *junction.ContextRegistry.
//
// Outputs:
// - returns []map[string]any.
//
// Contract:
// - Returns exactly one `[]map[string]any`.
// - Emits no response, trace, or outbound message.
// - Wrapper state is read under wrapper lock and emitted in sorted wrapper-name order.

func snapshotWrappers(frame *junction.ContextRegistry) []map[string]any {
	if frame == nil || frame.Wrappers == nil {
		return []map[string]any{}
	}
	names := make([]string, 0, len(frame.Wrappers))
	for k := range frame.Wrappers {
		if strings.TrimSpace(k) != "" {
			names = append(names, k)
		}
	}
	sortStrings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		st := frame.Wrappers[name]
		if st == nil {
			continue
		}

		st.Lock()
		item := map[string]any{
			circulation.KeyName:       name,
			circulation.KeyProcState:  fmt.Sprintf("%v", st.ProcState),
			circulation.KeyPID:        st.PID,
			circulation.KeyReady:      st.Ready,
			circulation.KeyBuilding:   st.GetBuilding(),
			circulation.KeyStarting:   st.GetStarting(),
			circulation.KeyBuildAt:    timeOrEmpty(st.BuiltAt),
			circulation.KeyStartedAt:  timeOrEmpty(st.StartedAt),
			circulation.KeyReadyAt:    timeOrEmpty(st.ReadyAt),
			circulation.KeyLastExitAt: timeOrEmpty(st.LastExitAt),
		}
		if !st.StopRequestedAt.IsZero() {
			item[circulation.KeyStopRequestedAt] = timeOrEmpty(st.StopRequestedAt)
		}
		if st.LastError != nil {
			item[circulation.KeyLastError] = st.LastError.Error()
		}
		if st.ReadyErr != nil {
			item[circulation.KeyReadyError] = st.ReadyErr.Error()
		}
		st.Unlock()

		out = append(out, item)
	}
	return out
}

// timeOrEmpty
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if time is zero: return empty string
//   - convert time to UTC
//   - render time as RFC3339Nano string
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - t time.Time.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Zero time maps to the empty string.

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// safeStr
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim surrounding whitespace from the input string
//   - return the normalized string
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - s string.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.

func safeStr(s string) string { return strings.TrimSpace(s) }

// sortStrings
//
// Functional role (Brique DSL):
// - >sequence:
//   - iterate over the string slice
//   - shift out-of-order items backward
//   - leave the input slice sorted in ascending order
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates the input slice in place.
//
// Inputs:
// - a []string.
//
// Outputs:
// - no direct return value; mutates input slice in place.
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Uses in-place insertion sort.

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		j := i
		for j > 0 && a[j] < a[j-1] {
			a[j], a[j-1] = a[j-1], a[j]
			j--
		}
	}
}

// capReadCapacity
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/read.capacity.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capReadCapacity(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.cap_name is required"))
		return
	}
	capName, _ := in.Params["cap_name"].(string)
	capName = strings.TrimSpace(capName)
	if capName == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.cap_name is required"))
		return
	}

	b, err := loadEngineCapacityJSON(capName)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeNotFound,
			map[string]any{circulation.KeyReason: "capacity_not_found", "cap_name": capName},
			err.Error()))
		return
	}

	obj, err := parseCapacityJSON(b)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: "capacity_parse_failed"},
			err.Error()))
		return
	}

	includeRaw, _ := in.Params[circulation.KeyIncludeRaw].(bool)

	detail, _ := in.Params[circulation.KeyDetailParam].(string)
	if strings.TrimSpace(strings.ToLower(detail)) != circulation.ValueFull {
		obj = invokeDetailCapacityObject(obj)
	}

	payload := map[string]any{
		"cap_name":   capName,
		"descriptor": obj,
	}
	if includeRaw {
		payload["descriptor_json"] = string(b)
	}

	l.emitResponseOK(in, payload)
}

// invokeDetailCapacityObject
//
// Functional role (Brique DSL):
// - reduce a parsed capacity descriptor's functional.#root to {role, inputs, outputs}, dropping narrative fields (effects, transformation_contract) not needed to build an invocation.
//
// Contract:
// - Only functional.#root is filtered; other top-level sections (brique, objective, subjective) and any sub-sections under #root are left untouched.
// - No-op when functional or functional.#root is absent or not an object.

func invokeDetailCapacityObject(obj map[string]any) map[string]any {
	functional, ok := obj[circulation.KeyFunctional].(map[string]any)
	if !ok {
		return obj
	}
	root, ok := functional[circulation.KeyDSLRoot].(map[string]any)
	if !ok {
		return obj
	}
	trimmedRoot := map[string]any{}
	for _, k := range []string{circulation.KeyRole, circulation.KeyInputs, circulation.KeyOutputs} {
		if v, present := root[k]; present {
			trimmedRoot[k] = v
		}
	}
	trimmedFunctional := map[string]any{}
	for k, v := range functional {
		if k == circulation.KeyDSLRoot {
			continue
		}
		trimmedFunctional[k] = v
	}
	trimmedFunctional[circulation.KeyDSLRoot] = trimmedRoot

	trimmedObj := map[string]any{}
	for k, v := range obj {
		if k == circulation.KeyFunctional {
			continue
		}
		trimmedObj[k] = v
	}
	trimmedObj[circulation.KeyFunctional] = trimmedFunctional
	return trimmedObj
}
