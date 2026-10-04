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

// reflexive/reflexive_edit.go
//
// Edition capabilities (Reflexive):
// - edit.patch_meaning
// - edit.delete
// - edit.create
// - edit.duplicate
// - edit.get_element_template
//
// Design constraints (from project docs):
// - Context-bound scope only (ctxDir is authoritative scope root).
// - No cross-context orchestration here (Comm does that).
// - Matter + Structure meaning/lifecycle are owned by MatterLoop:
//   - Reflexive MUST refuse edit.* and read.meaning for matter/structure.
//   - Reflexive MAY list matter/structure in read.structure (handled elsewhere).
// - For edit.patch_meaning: UI sends ONLY top-level sections among:
//     brique | objective | functional | subjective
//   Reflexive merges by REPLACING those sections in the current meaning document.
//   engine writes atomically (no patch DSL needed for this capability).
//
// NOTE: This file defines handlers, request/response shapes, and shared helpers.
// You still need to wire these caps in buildCapTable() in reflexive_loop.go:
//   "edit.patch_meaning"       -> (*ReflexiveLoop).capEditPatchMeaning
//   "edit.delete"              -> (*ReflexiveLoop).capEditDelete
//   "edit.create"              -> (*ReflexiveLoop).capEditCreate
//   "edit.duplicate"           -> (*ReflexiveLoop).capEditDuplicate
//   "edit.get_element_template"-> (*ReflexiveLoop).capEditGetElementTemplate

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/shared"
	"brique_engine/template"
)

// loadTemplateBytes
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim template name
//   - validate non-empty template identifier
//   - read embedded template bytes from template filesystem
//
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
// - reads embedded template bytes from `template.FS`.
//
// Inputs:
// - templateName string.
//
//
// Outputs:
// - returns ([]byte, error).
//
//
// Contract:
// - Returns exactly one `([]byte, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func loadTemplateBytes(templateName string) ([]byte, error) {
	name := strings.TrimSpace(templateName)
	if name == "" {
		return nil, fmt.Errorf("template name is empty")
	}
	// templateName is expected like: "context.json", "capacity.json", "message.json", etc.
	b, err := template.FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("template read failed: %s: %w", name, err)
	}
	return b, nil
}

// templateFilenameForType
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim the logical item type
//   - append `.json`
//   - return the template filename
//
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
// - t string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Trims surrounding spaces from the type before appending `.json`.

func templateFilenameForType(t string) string {
	// Template files are named like:
	//   <type><descriptorExt>
	// Example:
	//   capacity.json
	//   context.json
	//   schema.json
	//   document.json
	// plus message/intention/response templates.
	//
	// Descriptor ext is always ".json" in your conventions.
	return strings.TrimSpace(t) + ".json"
}

// -----------------------------
// Request shapes
// -----------------------------

type editItem struct {
	Type       string // required (capacity|context|schema|document|...)
	Name       string // for create/delete/patch
	SourceName string // for duplicate
	TargetName string // for duplicate
	// DestinationCtxID is an absolute context path (e.g. "/root/app_test") naming
	// the context that should receive the duplicate. For edit.duplicate only;
	// defaults to the addressed context when empty.
	DestinationCtxID string
	// For edit.patch_meaning: Patch is treated as a SECTION MERGE object (only 4 known sections).
	// For edit.duplicate: Patch is NOT supported (UI duplicates then calls edit.patch_meaning).
	Patch         any
	PatchJSON     string
	SemanticPatch any
	Content       map[string]any // for create: meaning json (no document content here)
	ContentJSON   string
}

// parseItems
//
// Functional role (Brique DSL):
// - >sequence:
//   - read `params.items`
//   - coerce each item object into normalized editItem fields
//   - trim string identifiers and preserve patch/content payloads
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - params keys consumed directly:
//   - `circulation.KeyItems`
// - item object keys consumed directly:
//   - `circulation.KeyType`
//   - `circulation.KeyName`
//   - `circulation.KeySourceName`
//   - `circulation.KeyTargetName`
//   - `circulation.KeyPatch`
//   - `circulation.KeyContent`
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
// - allocates the returned `[]editItem`.
//
// Inputs:
// - params map[string]any.
//
//
// Outputs:
// - returns ([]editItem, error).
//
//
// Contract:
// - Returns exactly one `([]editItem, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func parseItems(params map[string]any) ([]editItem, error) {
	raw, ok := params[circulation.KeyItems].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("params.items is required")
	}

	out := make([]editItem, 0, len(raw))
	for _, it := range raw {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		e := editItem{}
		if v, ok := m[circulation.KeyType].(string); ok {
			e.Type = strings.TrimSpace(strings.ToLower(v))
		}
		if v, ok := m[circulation.KeyName].(string); ok {
			e.Name = strings.TrimSpace(v)
		}
		if v, ok := m[circulation.KeySourceName].(string); ok {
			e.SourceName = strings.TrimSpace(v)
		}
		if v, ok := m[circulation.KeyTargetName].(string); ok {
			e.TargetName = strings.TrimSpace(v)
		}
		if v, ok := m[circulation.KeyDestinationCtxId].(string); ok {
			e.DestinationCtxID = strings.TrimSpace(v)
		}
		if v, ok := m[circulation.KeyPatch]; ok {
			e.Patch = v
		}
		if v, ok := m[circulation.KeyPatchJSON].(string); ok {
			e.PatchJSON = v
		}
		if v, ok := m[circulation.KeySemanticPatch]; ok {
			e.SemanticPatch = v
		}
		if v, ok := m[circulation.KeyContent].(map[string]any); ok {
			e.Content = v
		}
		if v, ok := m[circulation.KeyContentJSON].(string); ok {
			e.ContentJSON = v
		}
		out = append(out, e)
	}
	return out, nil
}

// normType
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim surrounding whitespace from the item type
//   - lowercase the normalized type
//   - return the normalized type
//
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
// - t string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.

func normType(t string) string { return strings.TrimSpace(strings.ToLower(t)) }

// validateSupportedEditType
//
// Functional role (Brique DSL):
// - >sequence:
//   - normalize incoming item type
//   - admit supported reflexive-owned types
//   - refuse matter/structure as matter-loop-owned
//   - reject unsupported types
//
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
// - t string.
//
//
// Outputs:
// - returns (string, error).
//
//
// Contract:
// - Returns exactly one `(string, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func validateSupportedEditType(t string) (string, error) {
	t = normType(t)
	switch t {
	case circulation.ValueContext,
		circulation.ValueCapacity,
		circulation.ValueSchema,
		circulation.ValueDocument:
		return t, nil
	case circulation.ValueMatter, circulation.ValueStructure:
		return t, fmt.Errorf("%s is owned by matter loop", t)
	default:
		return t, fmt.Errorf("unsupported type %q", t)
	}
}

// editOK
//
// Functional role (Brique DSL):
// - >sequence:
//   - allocate a result map when needed
//   - mark the item payload with `ok=true`
//   - return the result payload
//
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
// - may mutate the provided `fields` map.
// - may allocate a result map when `fields` is nil.
//
// Inputs:
// - fields map[string]any.
//
//
// Outputs:
// - returns map[string]any.
//
//
// Contract:
// - Returns exactly one `map[string]any`.
// - Emits no response, trace, or outbound message.
// - Mutates or allocates the result map and sets `circulation.KeyOK = true`.

func editOK(fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	fields[circulation.KeyOK] = true
	return fields
}

// editErr
//
// Functional role (Brique DSL):
// - >sequence:
//   - allocate a result map when needed
//   - mark the item payload with `ok=false`
//   - attach the normalized nested error object
//   - return the result payload
//
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
// - may mutate the provided `fields` map.
// - may allocate a result map when `fields` is nil.
//
// Inputs:
// - fields map[string]any, code, msg string, details map[string]any.
//
//
// Outputs:
// - returns map[string]any.
//
//
// Contract:
// - Returns exactly one `map[string]any`.
// - Emits no response, trace, or outbound message.
// - Mutates or allocates the result map, sets `ok=false`, and writes `error.{code,message,details}`.

func editErr(fields map[string]any, code, msg string, details map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	fields[circulation.KeyOK] = false
	fields[circulation.KeyError] = map[string]any{
		circulation.KeyCode:    code,
		circulation.KeyMessage: msg,
		circulation.KeyDetails: details,
	}
	return fields
}

// meaningPathFor
//
// Functional role (Brique DSL):
// - >switch on normalized item type:
//   - resolve context/capacity/schema/document meaning descriptor path
//   - reject matter/structure as matter-loop-owned
//   - reject unsupported or missing identifiers
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - ctxDir, t, id string.
//
//
// Outputs:
// - returns (diskPath string, navPath string, err error).
//
//
// Contract:
// - Returns both absolute disk path and slash-normalized navigation path.

func meaningPathFor(ctxDir, t, id string) (diskPath string, navPath string, err error) {
	t = normType(t)
	id = strings.TrimSpace(id)

	switch t {
	case circulation.ValueContext:
		// context meaning is always context.json
		navPath = ContextDescriptorFilename
		diskPath = filepath.Join(ctxDir, ContextDescriptorFilename)
		return
	case circulation.ValueCapacity:
		if id == "" {
			return "", "", fmt.Errorf("capacity name is required")
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueCapacity, id+CapacityDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueCapacity, id+CapacityDescriptorExt)
		return
	case circulation.ValueSchema:
		if id == "" {
			return "", "", fmt.Errorf("schema name is required")
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueSchema, id+SchemaDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueSchema, id+SchemaDescriptorExt)
		return
	case circulation.ValueDocument:
		if id == "" {
			return "", "", fmt.Errorf("document name is required")
		}
		navPath = filepath.ToSlash(filepath.Join(circulation.ValueDocument, id+DocumentDescriptorExt))
		diskPath = filepath.Join(ctxDir, circulation.ValueDocument, id+DocumentDescriptorExt)
		return
	case circulation.ValueMatter, circulation.ValueStructure:
		return "", "", fmt.Errorf("%s is owned by matter loop", t)
	default:
		return "", "", fmt.Errorf("unsupported type %q", t)
	}
}

// contextChildDir
//
// Functional role (Brique DSL):
// - validate child context name and resolve existing child context directory under current context.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - ctxDir, child string.
//
//
// Outputs:
// - returns (string, error).
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func contextChildDir(ctxDir, child string) (string, error) {
	child = strings.TrimSpace(child)
	if child == "" || child == "." || child == ".." {
		return "", fmt.Errorf("invalid context name")
	}
	if strings.Contains(child, "/") || strings.Contains(child, "\\") {
		return "", fmt.Errorf("context name must be a single segment")
	}
	dir := filepath.Join(ctxDir, child)
	// must be a directory containing context.json
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("context not found")
	}
	if _, err := os.Stat(filepath.Join(dir, ContextDescriptorFilename)); err != nil {
		return "", fmt.Errorf("context not found")
	}
	return dir, nil
}

// resolveDestinationContextDir
//
// Functional role (Brique DSL):
// - resolve an absolute context id (e.g. "/root/app_test") to its existing
//   context directory on disk, scoped to the current instance root.
//
// Inputs:
// - selfCtxID, selfCtxDir, destCtxID string.
//
// Outputs:
// - returns (string, error).
//
// Contract:
// - selfCtxID/selfCtxDir locate the instance root ("/root" -> the directory
//   that is selfCtxDir with selfCtxID's "/root"-relative suffix stripped).
// - destCtxID must be an absolute path rooted at shared.RootContextID.
// - Reports failure via returned `error`; callers convert it to the
//   appropriate circulation response semantics.

func resolveDestinationContextDir(selfCtxID, selfCtxDir, destCtxID string) (string, error) {
	destCtxID = strings.TrimSpace(destCtxID)
	if !strings.HasPrefix(destCtxID, shared.RootContextID) {
		return "", fmt.Errorf("destination_ctx_id must be an absolute path starting with %q", shared.RootContextID)
	}

	selfCtxID = strings.TrimSpace(selfCtxID)
	selfRel := strings.TrimPrefix(selfCtxID, shared.RootContextID)
	rootDir := strings.TrimSuffix(selfCtxDir, filepath.FromSlash(selfRel))

	destRel := strings.TrimPrefix(strings.TrimPrefix(destCtxID, shared.RootContextID), "/")
	destDir := filepath.Join(rootDir, filepath.FromSlash(destRel))

	if ok, err := ensureWithinDir(rootDir, destDir); err != nil || !ok {
		return "", fmt.Errorf("destination_ctx_id out of scope")
	}
	st, err := os.Stat(destDir)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("destination context not found")
	}
	if _, err := os.Stat(filepath.Join(destDir, ContextDescriptorFilename)); err != nil {
		return "", fmt.Errorf("destination context not found")
	}
	return destDir, nil
}

func updateParentChildList(ctxDir string, child string, present bool) error {
	child = strings.TrimSpace(child)
	if child == "" || strings.Contains(child, "/") || strings.Contains(child, "\\") {
		return fmt.Errorf("invalid child context name")
	}

	descPath := filepath.Join(ctxDir, ContextDescriptorFilename)
	b, err := os.ReadFile(descPath)
	if err != nil {
		return err
	}
	doc, err := parseOrderedJSON(b)
	if err != nil || doc == nil || doc.kind != 'o' {
		if err == nil {
			err = fmt.Errorf("invalid json object")
		}
		return err
	}
	syn := doc.objectValue(circulation.KeyBrique)

	set := map[string]struct{}{}
	if raw, ok := syn.get(configuration.KeyChildList); ok && raw.kind == 'a' {
		for _, it := range raw.array {
			if s, ok := it.scalar.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					set[s] = struct{}{}
				}
			}
		}
	}
	if present {
		set[child] = struct{}{}
	} else {
		delete(set, child)
	}

	list := make([]string, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	sort.Strings(list)
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	syn.set(configuration.KeyChildList, orderedJSONFromAny(out))
	return atomicWriteOrderedJSON(descPath, doc)
}

// ensureDirForFile
//
// Functional role (Brique DSL):
// - ensure parent directory exists for a target file path.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - absPath string.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func ensureDirForFile(absPath string) error {
	dir := filepath.Dir(absPath)
	return os.MkdirAll(dir, 0o755)
}

// atomicWriteFile
//
// Functional role (Brique DSL):
// - >sequence:
//   - ensure parent directory exists
//   - write data into sibling temporary file
//   - atomically replace target path with temporary file
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Inputs:
// - absPath string, data []byte, perm os.FileMode.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Reports failure via returned `error`; callers convert it to the appropriate circulation response semantics.

func atomicWriteFile(absPath string, data []byte, perm os.FileMode) error {
	if err := ensureDirForFile(absPath); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(absPath), "."+filepath.Base(absPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := shared.AtomicReplace(tmp, absPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (l *ReflexiveLoop) lockEditPath(absPath string) func() {
	l.editLocksMu.Lock()
	mu := l.editLocks[absPath]
	if mu == nil {
		mu = &sync.Mutex{}
		l.editLocks[absPath] = mu
	}
	l.editLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// copyFileAtomic
//
// Functional role (Brique DSL):
// - >sequence:
//   - ensure the destination parent directory exists
//   - open the source file
//   - stream-copy source bytes into a sibling temporary destination
//   - close the temporary file
//   - atomically replace the target file with the temporary file
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
// - writes `dstAbs` atomically via a temporary sibling file and `shared.AtomicReplace`.
//
// Inputs:
// - srcAbs, dstAbs string.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func copyFileAtomic(srcAbs, dstAbs string) error {
	if err := ensureDirForFile(dstAbs); err != nil {
		return err
	}
	in, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dstAbs + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, cErr := io.Copy(out, in)
	closeErr := out.Close()
	if cErr != nil {
		_ = os.Remove(tmp)
		return cErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := shared.AtomicReplace(tmp, dstAbs); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

// removeAllIfExists
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate non-empty target path
//   - stat the target path
//   - >if target is missing: return success
//   - remove the file or directory tree
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
// - may remove the file or directory tree at `absPath`.
//
// Inputs:
// - absPath string.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Missing targets are treated as success.

func removeAllIfExists(absPath string) error {
	if strings.TrimSpace(absPath) == "" {
		return fmt.Errorf("empty path")
	}
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(absPath)
}

// deepCopyJSON
//
// Functional role (Brique DSL):
// - >sequence:
//   - marshal the input value to JSON
//   - unmarshal the JSON bytes into a fresh value
//   - return the cloned JSON-compatible value
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
// - allocates the returned cloned value.
//
// Inputs:
// - v any.
//
// Outputs:
// - returns any.
//
// Contract:
// - Returns exactly one cloned value.
// - Emits no response, trace, or outbound message.
// - Best-effort copy for JSON-shaped values; marshal/unmarshal errors are ignored and may yield nil or zero-shape output.

func deepCopyJSON(v any) any {
	// Safe & simple for JSON-y values
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// ensureDocumentLocalDefault
//
// Functional role (Brique DSL):
// - >sequence:
//   - ensure the `brique` section exists
//   - >if `brique.ref` is set: leave the meaning unchanged
//   - >if `brique.file` is already set: leave the meaning unchanged
//   - derive the default local file path from `docID`
//   - write the default `brique.file`
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
// - may allocate or mutate the `brique` section of `meaning`.
//
// Inputs:
// - docID string, meaning map[string]any.
//
// Outputs:
// - no direct return value; mutates `meaning` in place when default applies.
//
// Contract:
// - Emits no response, trace, or outbound message.
// - Leaves `meaning` unchanged when `brique.ref` or `brique.file` is already set, or when `docID` is empty.

func ensureDocumentLocalDefault(docID string, meaning map[string]any) {
	if meaning == nil {
		return
	}
	syn, _ := meaning[circulation.KeyBrique].(map[string]any)
	if syn == nil {
		syn = map[string]any{}
		meaning[circulation.KeyBrique] = syn
	}

	// If external ref exists => do nothing (ref doc)
	if v, ok := syn[circulation.KeyRef].(string); ok && strings.TrimSpace(v) != "" {
		return
	}

	// If local file already present => do nothing
	if v, ok := syn[circulation.KeyFile].(string); ok && strings.TrimSpace(v) != "" {
		return
	}

	// Deduce local by default: document/<docID>.md
	docID = strings.TrimSpace(docID)
	if docID == "" {
		return
	}
	rel := filepath.ToSlash(filepath.Join(circulation.ValueDocument, docID+".md"))
	syn[circulation.KeyFile] = rel
}

// docLocalPathFromMeaning
//
// Functional role (Brique DSL):
// - >sequence:
//   - require a non-nil meaning document with a `brique` section
//   - >if `brique.ref` is set: classify the document as external
//   - require and normalize `brique.file` for local documents
//   - enforce that the local path stays under `document/`
//   - derive the absolute local file path under `ctxDir`
//   - return local-path metadata or an error
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
// - ctxDir, docID string, meaning map[string]any.
//
// Outputs:
// - returns (rel string, abs string, isLocal bool, err error).
//
// Contract:
// - Returns exactly one `(rel, abs, isLocal, err)` tuple.
// - Emits no response, trace, or outbound message.
// - Returns `isLocal=false` for external refs and a validation error for missing or invalid local declarations.

func docLocalPathFromMeaning(ctxDir, docID string, meaning map[string]any) (rel string, abs string, isLocal bool, err error) {

	if meaning == nil {
		return "", "", false, fmt.Errorf("missing meaning")
	}

	syn, _ := meaning[circulation.KeyBrique].(map[string]any)
	if syn != nil {

		// External reference => not local
		if v, ok := syn[circulation.KeyRef].(string); ok && strings.TrimSpace(v) != "" {
			return "", "", false, nil
		}

		// Local file MUST be explicit and valid
		vAny, ok := syn[circulation.KeyFile]
		if !ok {
			return "", "", false, fmt.Errorf("missing brique.file")
		}
		v, _ := vAny.(string)
		v = strings.TrimSpace(v)
		if v == "" {
			return "", "", false, fmt.Errorf("invalid brique.file")
		}
		rel = normalizeRelPath(v)
		if rel == "" {
			return "", "", false, fmt.Errorf("invalid brique.file")
		}

		// Enforce under document/
		if rel != circulation.ValueDocument && !strings.HasPrefix(rel, circulation.ValueDocument+"/") {
			rel = filepath.ToSlash(filepath.Join(circulation.ValueDocument, rel))
		}

		abs = filepath.Join(ctxDir, filepath.FromSlash(rel))
		return rel, abs, true, nil
	}

	// If brique section is missing, we consider it invalid for document locality.
	return "", "", false, fmt.Errorf("missing brique section")
}

// normalizeRelPath
//
// Functional role (Brique DSL):
// - >sequence:
//   - trim and slash-normalize the input path
//   - reject UNC, drive-qualified, absolute, and traversal paths
//   - clean the relative path
//   - return the normalized relative path or empty string
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
// - p string.
//
// Outputs:
// - returns string.
//
// Contract:
// - Returns exactly one string.
// - Emits no response, trace, or outbound message.
// - Returns the empty string on invalid path forms.

func normalizeRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Reject obvious absolute/drive/UNC patterns deterministically.
	// (We normalize slashes first, then validate.)
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "//") {
		return ""
	}
	if strings.Contains(p, ":") {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// validateMeaningInvariants
//
// Functional role (Brique DSL):
// - validate minimal meaning invariants before write:
//   - `brique` section must be an object when present
//   - `brique.kind` must match item type when present, except capacities
//     where kind names the execution style (`dsl|compiled|interpreted`)
//
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
// - itemType string, doc map[string]any.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func validateMeaningInvariants(itemType string, doc map[string]any) error {
	if doc == nil {
		return fmt.Errorf("nil meaning")
	}
	// If brique exists, it must be an object.
	if v, ok := doc[circulation.KeyBrique]; ok && v != nil {
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("brique must be an object")
		}
	}
	// Best-effort: if brique.kind exists, it must match the element type.
	// Capacity descriptors use brique.kind for execution style, not element type.
	if syn, _ := doc[circulation.KeyBrique].(map[string]any); syn != nil {
		if k, ok := syn[circulation.KeyKind].(string); ok && strings.TrimSpace(k) != "" {
			if !isValidBriqueKindForEditType(itemType, k) {
				return fmt.Errorf("brique.kind mismatch (got %q, expected %q)", k, itemType)
			}
		}
	}
	return nil
}

func isValidBriqueKindForEditType(itemType, kind string) bool {
	itemType = normType(itemType)
	kind = normType(kind)
	if itemType == circulation.ValueCapacity {
		return kind == circulation.ValueCapacity ||
			kind == configuration.ValueConfigStyleDSL ||
			kind == configuration.ValueConfigStyleCompiled ||
			kind == configuration.ValueConfigStyleInterpreted
	}
	return kind == itemType
}

// asMap
//
// Functional role (Brique DSL):
// - >sequence:
//   - >if the input is already a non-nil `map[string]any`: return it
//   - marshal the value to JSON
//   - unmarshal the JSON bytes into a map
//   - return the coerced map or an error
//
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
// - allocates the returned map when coercion succeeds through JSON round-trip.
//
// Inputs:
// - v any.
//
//
// Outputs:
// - returns (map[string]any, error).
//
//
// Contract:
// - Returns exactly one `(map[string]any, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func asMap(v any) (map[string]any, error) {
	if v == nil {
		return nil, fmt.Errorf("nil")
	}
	if m, ok := v.(map[string]any); ok && m != nil {
		return m, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, fmt.Errorf("not an object")
	}
	return m, nil
}

var allowedMeaningSections = map[string]bool{
	circulation.KeyBrique:       true,
	circulation.ValueObjective:  true,
	circulation.ValueFunctional: true,
	circulation.ValueSubjective: true,
}

// isAllowedMeaningSection
//
// Functional role (Brique DSL):
// - >sequence:
//   - look up the requested section key in the allow-list
//   - return whether the section is patchable
//
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
// - k string.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Returns exactly one boolean.
// - Emits no response, trace, or outbound message.

func isAllowedMeaningSection(k string) bool { return allowedMeaningSections[k] }

// parseSectionPatch
//
// Functional role (Brique DSL):
// - >sequence:
//   - require non-nil patch payload
//   - coerce patch to object
//   - admit only allowed top-level sections
//   - validate each section value as object or null
//   - require at least one allowed section
//
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
// - allocates the returned patch map when parsing succeeds.
//
// Inputs:
// - it editItem.
//
//
// Outputs:
// - returns (map[string]any, error).
//
//
// Contract:
// - Returns exactly one `(map[string]any, error)` pair.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.

func parseSectionPatch(it editItem) (map[string]any, error) {
	if it.Patch == nil {
		return nil, fmt.Errorf("missing patch")
	}
	p, err := asMap(it.Patch)
	if err != nil {
		return nil, fmt.Errorf("patch must be an object")
	}

	// Strict: no extra keys + at least one allowed section.
	hasAllowed := false
	for k, v := range p {
		if !isAllowedMeaningSection(k) {
			return nil, fmt.Errorf("unknown top-level section %q (only brique|objective|functional|subjective allowed)", k)
		}
		hasAllowed = true

		// validate section value: object or null
		if v == nil {
			continue
		}
		if _, ok := v.(map[string]any); ok {
			continue
		}
		// Allow coercion for json-y values (e.g. map[string]interface{} already ok; structs, etc.)
		if _, err := asMap(v); err != nil {
			return nil, fmt.Errorf("section %q must be an object (or null)", k)
		}
	}
	if !hasAllowed {
		return nil, fmt.Errorf("patch must include at least one section among brique|objective|functional|subjective")
	}
	return p, nil
}

func applyOrderedSemanticPatch(doc *orderedJSONValue, raw any) (*orderedJSONValue, error) {
	patch, err := shared.ParseSemanticPatch(raw)
	if err != nil {
		return nil, err
	}
	result, err := shared.ApplySemanticPatch(doc, patch, semanticMeaningPatchOptions())
	if err != nil {
		return nil, err
	}
	ordered, ok := result.(*orderedJSONValue)
	if !ok {
		return nil, fmt.Errorf("semantic patch result root is not ordered json")
	}
	return ordered, nil
}

func semanticMeaningPatchOptions() shared.SemanticPatchOptions {
	roots := map[string]bool{
		circulation.KeyBrique:     true,
		circulation.KeyObjective:  true,
		circulation.KeyFunctional: true,
		circulation.KeySubjective: true,
	}
	return shared.SemanticPatchOptions{AllowedRoots: roots, ProtectedRoots: roots, CreateObjects: true}
}

// capEditPatchMeaning
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/edit.patch_meaning.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capEditPatchMeaning(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.items is required"))
		return
	}
	items, err := parseItems(in.Params)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidParams},
			err.Error()))
		return
	}

	results := make([]map[string]any, 0, len(items))
	for _, it := range items {
		t, terr := validateSupportedEditType(it.Type)
		name := strings.TrimSpace(it.Name)

		base := map[string]any{
			circulation.KeyType: t,
			circulation.KeyName: name,
		}

		if terr != nil {
			// matter/structure refused; unknown invalid
			code := circulation.ValueCodeInvalid
			if t == circulation.ValueMatter || t == circulation.ValueStructure {
				code = circulation.ValueCodeRefused
			}
			results = append(results, editErr(base, code, terr.Error(), map[string]any{circulation.KeyReason: "unsupported_type"}))
			continue
		}
		if t != circulation.ValueContext && name == "" {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "name is required", map[string]any{circulation.KeyReason: "missing_name"}))
			continue
		}

		abs, _, perr := meaningPathFor(l.frame.ContextDir, t, name)
		if perr != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, perr.Error(), map[string]any{circulation.KeyReason: "resolve_failed"}))
			continue
		}
		if ok, e := ensureWithinDir(l.frame.ContextDir, abs); e != nil || !ok {
			results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
			continue
		}
		unlockEdit := l.lockEditPath(abs)

		if _, statErr := os.Stat(abs); statErr != nil {
			code := circulation.ValueCodeNotFound
			if !os.IsNotExist(statErr) {
				code = circulation.ValueCodeReadFail
			}
			results = append(results, editErr(base, code, statErr.Error(), map[string]any{circulation.KeyReason: "not_found"}))
			unlockEdit()
			continue
		}

		// Load current meaning as source of truth
		bCur, rerr := os.ReadFile(abs)
		if rerr != nil {
			code := circulation.ValueCodeNotFound
			if !os.IsNotExist(rerr) {
				code = circulation.ValueCodeReadFail
			}
			results = append(results, editErr(base, code, rerr.Error(), map[string]any{circulation.KeyReason: "read_failed"}))
			unlockEdit()
			continue
		}
		curOrdered, err := parseOrderedJSON(bCur)
		if err != nil || curOrdered == nil || curOrdered.kind != 'o' {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "invalid json", map[string]any{circulation.KeyReason: "invalid_json"}))
			unlockEdit()
			continue
		}

		if it.PatchJSON != "" {
			parsedPatch, perr := parseOrderedJSON([]byte(it.PatchJSON))
			if perr != nil {
				msg, details := jsonParseErrorDetail(perr, it.PatchJSON)
				results = append(results, editErr(base, circulation.ValueCodeInvalid, msg, details))
				unlockEdit()
				continue
			}
			if parsedPatch.kind != 'o' {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "patch_json must be a json object (top-level value is not an object)", map[string]any{circulation.KeyReason: "invalid_payload"}))
				unlockEdit()
				continue
			}
			it.Patch = parsedPatch.toAny()
		}
		if it.Patch == nil && it.SemanticPatch == nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "missing patch or semantic_patch", map[string]any{circulation.KeyReason: "invalid_payload"}))
			unlockEdit()
			continue
		}

		if it.Patch != nil {
			patch, derr := parseSectionPatch(it)
			if derr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, derr.Error(), map[string]any{circulation.KeyReason: "invalid_payload"}))
				unlockEdit()
				continue
			}
			orderedPatch, derr := orderedJSONForRequest(it.PatchJSON, patch)
			if derr != nil {
				msg, details := jsonParseErrorDetail(derr, it.PatchJSON)
				results = append(results, editErr(base, circulation.ValueCodeInvalid, msg, details))
				unlockEdit()
				continue
			}
			if orderedPatch.kind != 'o' {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "patch must be a json object (top-level value is not an object)", map[string]any{circulation.KeyReason: "invalid_payload"}))
				unlockEdit()
				continue
			}

			// Apply patch: merge by REPLACING provided sections only (no other keys exist by contract).
			for _, entry := range orderedPatch.object {
				if !isAllowedMeaningSection(entry.key) {
					continue
				}
				curOrdered.set(entry.key, entry.value)
			}
		}

		if it.SemanticPatch != nil {
			patched, serr := applyOrderedSemanticPatch(curOrdered, it.SemanticPatch)
			if serr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, serr.Error(), map[string]any{circulation.KeyReason: "invalid_payload"}))
				unlockEdit()
				continue
			}
			curOrdered = patched
		}

		cur, _ := curOrdered.toAny().(map[string]any)
		if ivErr := validateMeaningInvariants(t, cur); ivErr != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, ivErr.Error(), map[string]any{circulation.KeyReason: "invariants_failed"}))
			unlockEdit()
			continue
		}

		if werr := atomicWriteOrderedJSON(abs, curOrdered); werr != nil {
			results = append(results, editErr(
				base,
				circulation.ValueCodeInternal,
				werr.Error(),
				map[string]any{circulation.KeyReason: "write_failed"},
			))
			unlockEdit()
			continue
		}

		results = append(results, editOK(base))
		unlockEdit()
		continue
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// capEditDelete
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/edit.delete.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capEditDelete(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.items is required"))
		return
	}
	items, err := parseItems(in.Params)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidParams},
			err.Error()))
		return
	}

	results := make([]map[string]any, 0, len(items))
	for _, it := range items {
		t, terr := validateSupportedEditType(it.Type)
		name := strings.TrimSpace(it.Name)

		base := map[string]any{
			circulation.KeyType: t,
			circulation.KeyName: name,
		}

		if terr != nil {
			code := circulation.ValueCodeInvalid
			if t == circulation.ValueMatter || t == circulation.ValueStructure {
				code = circulation.ValueCodeRefused
			}
			results = append(results, editErr(base, code, terr.Error(), map[string]any{circulation.KeyReason: "unsupported_type"}))
			continue
		}

		// Context delete = delete whole child context directory tree.
		if t == circulation.ValueContext {
			if name == "" || name == "." {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "context delete requires a child context name", map[string]any{circulation.KeyReason: "missing_name"}))
				continue
			}
			childDir, cerr := contextChildDir(l.frame.ContextDir, name)
			if cerr != nil {
				results = append(results, editErr(base, circulation.ValueCodeNotFound, cerr.Error(), map[string]any{circulation.KeyReason: "not_found"}))
				continue
			}
			if ok, e := ensureWithinDir(l.frame.ContextDir, childDir); e != nil || !ok {
				results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
				continue
			}
			if derr := removeAllIfExists(childDir); derr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, derr.Error(), map[string]any{circulation.KeyReason: "delete_failed"}))
				continue
			}
			if uerr := updateParentChildList(l.frame.ContextDir, name, false); uerr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, uerr.Error(), map[string]any{circulation.KeyReason: "parent_update_failed"}))
				continue
			}
			results = append(results, editOK(base))
			continue
		}

		absMeaning, _, perr := meaningPathFor(l.frame.ContextDir, t, name)
		if perr != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, perr.Error(), map[string]any{circulation.KeyReason: "resolve_failed"}))
			continue
		}
		if ok, e := ensureWithinDir(l.frame.ContextDir, absMeaning); e != nil || !ok {
			results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
			continue
		}

		// Special: document deletion = meaning + local file (if local)
		if t == circulation.ValueDocument {
			// best-effort load meaning to decide local file
			var meaning map[string]any
			if b, err := os.ReadFile(absMeaning); err == nil {
				_ = json.Unmarshal(b, &meaning)
			}
			_, absDoc, isLocal, derr := docLocalPathFromMeaning(l.frame.ContextDir, name, meaning)
			if derr != nil && meaning != nil {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, derr.Error(), map[string]any{circulation.KeyReason: "invalid_doc_path"}))
				continue
			}
			// delete meaning first, then file (or reverse? doesn't matter; keep deterministic)
			if err := removeAllIfExists(absMeaning); err != nil {
				code := circulation.ValueCodeInternal
				if os.IsNotExist(err) {
					code = circulation.ValueCodeNotFound
				}
				results = append(results, editErr(base, code, err.Error(), map[string]any{circulation.KeyReason: "delete_failed"}))
				continue
			}
			if isLocal && absDoc != "" {
				// Only delete if under ctxDir
				if ok, e := ensureWithinDir(l.frame.ContextDir, absDoc); e == nil && ok {
					_ = removeAllIfExists(absDoc)
				}
			}
			results = append(results, editOK(base))
			continue
		}

		// Standard: delete meaning file
		if err := removeAllIfExists(absMeaning); err != nil {
			code := circulation.ValueCodeInternal
			if os.IsNotExist(err) {
				code = circulation.ValueCodeNotFound
			}
			results = append(results, editErr(base, code, err.Error(), map[string]any{circulation.KeyReason: "delete_failed"}))
			continue
		}
		results = append(results, editOK(base))
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// capEditCreate
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/edit.create.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capEditCreate(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.items is required"))
		return
	}
	items, err := parseItems(in.Params)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidParams},
			err.Error()))
		return
	}

	results := make([]map[string]any, 0, len(items))

	for _, it := range items {
		// item-scoped vars (avoid bleed across items)
		var (
			absMeaning     string
			perr           error
			meaning        map[string]any
			meaningOrdered *orderedJSONValue
		)
		t, terr := validateSupportedEditType(it.Type)
		name := strings.TrimSpace(it.Name)

		base := map[string]any{
			circulation.KeyType: t,
			circulation.KeyName: name,
		}

		if terr != nil {
			code := circulation.ValueCodeInvalid
			if t == circulation.ValueMatter || t == circulation.ValueStructure {
				code = circulation.ValueCodeRefused
			}
			results = append(results, editErr(base, code, terr.Error(), map[string]any{circulation.KeyReason: "unsupported_type"}))
			continue
		}

		// Context create = create child context tree: <ctxDir>/<name>/{context.json, document/, capacity/, schema/, matter/, structure/}
		if t == circulation.ValueContext {
			if name == "" {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "context create requires a child context name", map[string]any{circulation.KeyReason: "missing_name"}))
				continue
			}
			if strings.Contains(name, "/") || strings.Contains(name, "\\") {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "context name must be a single segment", map[string]any{circulation.KeyReason: "invalid_name"}))
				continue
			}
			childDir := filepath.Join(l.frame.ContextDir, name)
			if ok, e := ensureWithinDir(l.frame.ContextDir, childDir); e != nil || !ok {
				results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
				continue
			}
			if _, err := os.Stat(childDir); err == nil {
				results = append(results, editErr(base, circulation.ValueCodeConflict, "target already exists", map[string]any{circulation.KeyReason: "exists"}))
				continue
			}
			// mkdir tree
			if err := os.MkdirAll(childDir, 0o755); err != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "mkdir_failed"}))
				continue
			}
			// required subdirs
			subdirs := []string{
				circulation.ValueDocument,
				circulation.ValueCapacity,
				circulation.ValueSchema,
				circulation.ValueMatter,
				circulation.ValueStructure,
			}
			for _, sd := range subdirs {
				if err := os.MkdirAll(filepath.Join(childDir, sd), 0o755); err != nil {
					results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "mkdir_failed", "dir": sd}))
					goto nextItem
				}
			}

			// context.json from template, unless provided content is present
			if it.Content != nil || it.ContentJSON != "" {
				meaningOrdered, err = orderedJSONForRequest(it.ContentJSON, it.Content)
				if err != nil {
					msg, details := jsonParseErrorDetail(err, it.ContentJSON)
					results = append(results, editErr(base, circulation.ValueCodeInvalid, msg, details))
					goto nextItem
				}
				if meaningOrdered.kind != 'o' {
					results = append(results, editErr(base, circulation.ValueCodeInvalid, "content must be a json object (top-level value is not an object)", map[string]any{circulation.KeyReason: "invalid_payload"}))
					goto nextItem
				}
				meaning, _ = meaningOrdered.toAny().(map[string]any)
			} else {
				tb, err := loadTemplateBytes(templateFilenameForType(circulation.ValueContext))
				if err != nil {
					results = append(results, editErr(base, circulation.ValueCodeConfiguration, err.Error(), map[string]any{circulation.KeyReason: "template_load_failed"}))
					goto nextItem
				}
				meaningOrdered, err = parseOrderedJSON(tb)
				if err != nil || meaningOrdered.kind != 'o' {
					results = append(results, editErr(base, circulation.ValueCodeConfiguration, "invalid context template", map[string]any{circulation.KeyReason: "template_invalid"}))
					goto nextItem
				}
				meaning, _ = meaningOrdered.toAny().(map[string]any)
			}

			// Context create must stamp the created child logical name explicitly.
			if syn, _ := meaning[circulation.KeyBrique].(map[string]any); syn != nil {
				syn[configuration.KeyContextName] = name
			}
			meaningOrdered.objectValue(circulation.KeyBrique).set(configuration.KeyContextName, orderedJSONFromAny(name))
			if err := atomicWriteOrderedJSON(filepath.Join(childDir, ContextDescriptorFilename), meaningOrdered); err != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "write_failed"}))
				goto nextItem
			}
			if err := updateParentChildList(l.frame.ContextDir, name, true); err != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "parent_update_failed"}))
				goto nextItem
			}

			results = append(results, editOK(base))
			continue
		}

		// Non-context create: create meaning JSON (from provided content or template).
		if name == "" {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "name is required", map[string]any{circulation.KeyReason: "missing_name"}))
			continue
		}
		absMeaning, _, perr = meaningPathFor(l.frame.ContextDir, t, name)
		if perr != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, perr.Error(), map[string]any{circulation.KeyReason: "resolve_failed"}))
			continue
		}
		if ok, e := ensureWithinDir(l.frame.ContextDir, absMeaning); e != nil || !ok {
			results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
			continue
		}
		if _, err := os.Stat(absMeaning); err == nil {
			results = append(results, editErr(base, circulation.ValueCodeConflict, "target already exists", map[string]any{circulation.KeyReason: "exists"}))
			continue
		}

		if it.Content != nil || it.ContentJSON != "" {
			meaningOrdered, err = orderedJSONForRequest(it.ContentJSON, it.Content)
			if err != nil {
				msg, details := jsonParseErrorDetail(err, it.ContentJSON)
				results = append(results, editErr(base, circulation.ValueCodeInvalid, msg, details))
				continue
			}
			if meaningOrdered.kind != 'o' {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, "content must be a json object (top-level value is not an object)", map[string]any{circulation.KeyReason: "invalid_payload"}))
				continue
			}
			meaning, _ = meaningOrdered.toAny().(map[string]any)
		} else {
			tb, err := loadTemplateBytes(templateFilenameForType(t))
			if err != nil {
				results = append(results, editErr(base, circulation.ValueCodeConfiguration, err.Error(), map[string]any{circulation.KeyReason: "template_load_failed"}))
				continue
			}
			meaningOrdered, err = parseOrderedJSON(tb)
			if err != nil || meaningOrdered.kind != 'o' {
				results = append(results, editErr(base, circulation.ValueCodeConfiguration, "invalid template json", map[string]any{circulation.KeyReason: "template_invalid"}))
				continue
			}
			meaning, _ = meaningOrdered.toAny().(map[string]any)
		}

		// Document special: create empty local file referenced by brique.file (always empty).
		if t == circulation.ValueDocument {
			// If template has neither brique.ref nor brique.file => deduce local by default.
			ensureDocumentLocalDefault(name, meaning)
			rel, abs, isLocal, derr := docLocalPathFromMeaning(l.frame.ContextDir, name, meaning)
			if derr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, derr.Error(), map[string]any{circulation.KeyReason: "invalid_doc_path"}))
				continue
			}
			if isLocal {
				// ensure meaning brique.file is set to rel deterministically
				syn, _ := meaning[circulation.KeyBrique].(map[string]any)
				if syn == nil {
					syn = map[string]any{}
					meaning[circulation.KeyBrique] = syn
				}
				syn[circulation.KeyFile] = rel
				meaningOrdered.objectValue(circulation.KeyBrique).set(circulation.KeyFile, orderedJSONFromAny(rel))

				if ok, e := ensureWithinDir(l.frame.ContextDir, abs); e != nil || !ok {
					results = append(results, editErr(base, circulation.ValueCodeInvalid, "invalid document local path", map[string]any{circulation.KeyReason: "invalid_doc_path"}))
					continue
				}
				// create empty file atomically (only if not exists; conflict otherwise)
				if _, err := os.Stat(abs); err == nil {
					results = append(results, editErr(base, circulation.ValueCodeConflict, "a file already exists at the document's target content path, but no descriptor is catalogued for it yet; remove or rename that file, or pick a different name, before retrying edit.create", map[string]any{circulation.KeyReason: "doc_exists"}))
					continue
				}
				if err := atomicWriteFile(abs, []byte{}, 0o644); err != nil {
					results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "doc_write_failed"}))
					continue
				}
			}
		}

		// Write meaning JSON
		if err := atomicWriteOrderedJSON(absMeaning, meaningOrdered); err != nil {
			results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "write_failed"}))
			continue
		}

		results = append(results, editOK(base))
		continue

	nextItem:
		// label target for goto
		continue
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// capEditDuplicate
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/edit.duplicate.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capEditDuplicate(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.items is required"))
		return
	}
	items, err := parseItems(in.Params)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidParams},
			err.Error()))
		return
	}

	results := make([]map[string]any, 0, len(items))
	for _, it := range items {
		t, terr := validateSupportedEditType(it.Type)
		src := strings.TrimSpace(it.SourceName)
		dst := strings.TrimSpace(it.TargetName)
		destCtxID := strings.TrimSpace(it.DestinationCtxID)

		base := map[string]any{
			circulation.KeyType:       t,
			circulation.KeySourceName: src,
			circulation.KeyTargetName: dst,
		}
		if destCtxID != "" {
			base[circulation.KeyDestinationCtxId] = destCtxID
		}

		if terr != nil {
			code := circulation.ValueCodeInvalid
			if t == circulation.ValueMatter || t == circulation.ValueStructure {
				code = circulation.ValueCodeRefused
			}
			results = append(results, editErr(base, code, terr.Error(), map[string]any{circulation.KeyReason: circulation.ValueReasonUnsupportedItemType}))
			continue
		}
		if src == "" || dst == "" {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "source_name and target_name are required", map[string]any{circulation.KeyReason: "missing_name"}))
			continue
		}
		// Duplicate is a pure copy primitive now: no patch here.
		if it.Patch != nil || it.PatchJSON != "" {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "patch is not supported for duplicate; call edit.patch_meaning after", map[string]any{circulation.KeyReason: "patch_not_supported"}))
			continue
		}

		// Resolve the destination context directory. Defaults to the addressed
		// context (sibling duplicate) when destination_ctx_id is not provided.
		dstCtxDir := l.frame.ContextDir
		crossContext := false
		if destCtxID != "" {
			resolved, derr := resolveDestinationContextDir(l.frame.CtxId, l.frame.ContextDir, destCtxID)
			if derr != nil {
				results = append(results, editErr(base, circulation.ValueCodeNotFound, derr.Error(), map[string]any{circulation.KeyReason: "destination_not_found"}))
				continue
			}
			dstCtxDir = resolved
			crossContext = destCtxID != l.frame.CtxId
		}

		// Reject degenerate duplicate. Only meaningful within the same context:
		// across contexts, identical names refer to distinct elements.
		if !crossContext && strings.EqualFold(src, dst) {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "source_name and target_name must differ", map[string]any{circulation.KeyReason: "same_name"}))
			continue
		}

		// Context duplicate = duplicate whole child context directory.
		if t == circulation.ValueContext {
			srcDir, err := contextChildDir(l.frame.ContextDir, src)
			if err != nil {
				results = append(results, editErr(base, circulation.ValueCodeNotFound, err.Error(), map[string]any{circulation.KeyReason: "source_not_found"}))
				continue
			}
			dstDir := filepath.Join(dstCtxDir, dst)
			if _, err := os.Stat(dstDir); err == nil {
				results = append(results, editErr(base, circulation.ValueCodeConflict, "target already exists", map[string]any{circulation.KeyReason: "exists"}))
				continue
			}
			if ok, e := ensureWithinDir(dstCtxDir, dstDir); e != nil || !ok {
				results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
				continue
			}

			allow := func(rel string) bool {
				s := filepath.ToSlash(rel)

				// ignore ".git" at root or anywhere in path
				if s == ".git" || strings.HasPrefix(s, ".git/") || strings.Contains(s, "/.git/") || strings.HasSuffix(s, "/.git") {
					return false
				}
				return true
			}

			// copy directory recursively (excluding .git)
			if err := copyDirTree(srcDir, dstDir, allow); err != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "copy_failed"}))
				continue
			}
			if err := updateParentChildList(dstCtxDir, dst, true); err != nil {
				results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "parent_update_failed"}))
				continue
			}

			results = append(results, editOK(base))
			continue
		}

		// Non-context: duplicate meaning json, then handle doc local file copy.
		srcMeaningAbs, _, err := meaningPathFor(l.frame.ContextDir, t, src)
		if err != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, err.Error(), map[string]any{circulation.KeyReason: "resolve_failed"}))
			continue
		}
		dstMeaningAbs, _, err := meaningPathFor(dstCtxDir, t, dst)
		if err != nil {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, err.Error(), map[string]any{circulation.KeyReason: "resolve_failed"}))
			continue
		}
		if _, err := os.Stat(dstMeaningAbs); err == nil {
			results = append(results, editErr(base, circulation.ValueCodeConflict, "target already exists", map[string]any{circulation.KeyReason: "exists"}))
			continue
		}

		b, rerr := os.ReadFile(srcMeaningAbs)
		if rerr != nil {
			code := circulation.ValueCodeNotFound
			if !os.IsNotExist(rerr) {
				code = circulation.ValueCodeReadFail
			}
			results = append(results, editErr(base, code, rerr.Error(), map[string]any{circulation.KeyReason: "source_read_failed"}))
			continue
		}
		meaningOrdered, err := parseOrderedJSON(b)
		if err != nil || meaningOrdered == nil || meaningOrdered.kind != 'o' {
			results = append(results, editErr(base, circulation.ValueCodeInvalid, "invalid json", map[string]any{circulation.KeyReason: "invalid_json"}))
			continue
		}
		meaning, _ := meaningOrdered.toAny().(map[string]any)

		// Document special: copy local file if local; set brique.file deterministically to document/<dst>.
		if t == circulation.ValueDocument {
			srcRel, srcAbs, srcIsLocal, derr := docLocalPathFromMeaning(l.frame.ContextDir, src, meaning)
			if derr != nil {
				results = append(results, editErr(base, circulation.ValueCodeInvalid, derr.Error(), map[string]any{circulation.KeyReason: "invalid_doc_path"}))
				continue
			}
			if srcIsLocal {
				// brique.file contains the name with extension; preserve extension from source.
				ext := filepath.Ext(srcRel)
				// Contract: extension MUST be present in brique.file (no deduction here).
				if ext == "" {
					results = append(results, editErr(base,
						circulation.ValueCodeInvalid,
						"document brique.file must include an extension for local documents",
						map[string]any{circulation.KeyReason: "missing_extension", "brique_file": srcRel},
					))
					continue
				}

				// Always use document/<dst><ext> for the duplicated local file.
				dstRel := filepath.ToSlash(filepath.Join(circulation.ValueDocument, dst+ext))
				dstAbs := filepath.Join(dstCtxDir, filepath.FromSlash(dstRel))
				if ok, e := ensureWithinDir(dstCtxDir, dstAbs); e != nil || !ok {
					results = append(results, editErr(base, circulation.ValueCodeRefused, "scope violation", map[string]any{circulation.KeyReason: "scope_violation"}))
					continue
				}
				if _, err := os.Stat(dstAbs); err == nil {
					results = append(results, editErr(base, circulation.ValueCodeConflict, "target document file exists", map[string]any{circulation.KeyReason: "doc_exists"}))
					continue
				}
				// Ensure meaning references dstRel
				syn, _ := meaning[circulation.KeyBrique].(map[string]any)
				if syn == nil {
					syn = map[string]any{}
					meaning[circulation.KeyBrique] = syn
				}
				syn[circulation.KeyFile] = dstRel
				meaningOrdered.objectValue(circulation.KeyBrique).set(circulation.KeyFile, orderedJSONFromAny(dstRel))

				// Copy file
				if err := copyFileAtomic(srcAbs, dstAbs); err != nil {
					results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "doc_copy_failed"}))
					continue
				}
			}
		}

		// Write target meaning JSON
		if err := atomicWriteOrderedJSON(dstMeaningAbs, meaningOrdered); err != nil {
			results = append(results, editErr(base, circulation.ValueCodeInternal, err.Error(), map[string]any{circulation.KeyReason: "write_failed"}))
			continue
		}

		results = append(results, editOK(base))
	}

	l.emitResponseOK(in, map[string]any{
		circulation.KeyResult: results,
	})
}

// copyDirTree
//
// Functional role (Brique DSL):
// - >sequence:
//   - normalize source and destination directories
//   - ensure the destination root exists
//   - walk the source directory tree
//   - filter each relative path through `allow`
//   - create destination directories
//   - copy regular files atomically into the destination tree
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
// - creates directories and files under `dstDir`.
//
// Inputs:
// - srcDir, dstDir string, allow func(rel string) bool.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Reports failure via returned `error`; callers convert it to circulation response semantics.
// - A copy failure may leave partially created directories or files under `dstDir`.

func copyDirTree(srcDir, dstDir string, allow func(rel string) bool) error {
	srcDir = filepath.Clean(srcDir)
	dstDir = filepath.Clean(dstDir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if allow != nil && !allow(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dstPath := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		// file copy (stream) + atomic replace
		return copyFileAtomic(p, dstPath)
	})
}

// capEditGetElementTemplate
//
// External interaction contract source of truth:
// - engine/reflexive/capacity/edit.get_element_template.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *ReflexiveLoop) capEditGetElementTemplate(msg circulation.Message) {
	in := msg.Intention
	if in.Params == nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingParams},
			"params.item_type is required"))
		return
	}
	itemType, _ := in.Params[circulation.KeyItemType].(string)
	itemType = normType(itemType)
	if itemType == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingItemType},
			"params.item_type is required"))
		return
	}

	// Allow both element templates + message/intention/response templates.
	// Element types (edit-supported): context, capacity, schema, document, matter, structure.
	// Special templates: message, intention, response (as per your note).
	allowed := map[string]bool{
		circulation.ValueContext:   true,
		circulation.ValueCapacity:  true,
		circulation.ValueSchema:    true,
		circulation.ValueDocument:  true,
		circulation.ValueMatter:    true,
		circulation.ValueStructure: true,
		circulation.ValueMessage:   true,
		circulation.ValueIntention: true,
		circulation.ValueResponse:  true,
	}
	if !allowed[itemType] {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonUnsupportedItemType, circulation.KeyType: itemType},
			"unsupported item_type"))
		return
	}

	fn := templateFilenameForType(itemType)
	b, err := loadTemplateBytes(fn)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonTemplateLoadFailed, circulation.KeyTemplate: fn},
			err.Error()))
		return
	}

	// Return parsed json (so UI can edit structured); raw bytes only when requested.
	includeRaw, _ := in.Params[circulation.KeyIncludeRaw].(bool)

	var obj map[string]any
	_ = json.Unmarshal(b, &obj)

	payload := map[string]any{
		circulation.KeyElementKind: itemType,
		circulation.KeyTemplate:    obj,
	}
	if includeRaw {
		payload[circulation.KeyDescriptorJSON] = string(b)
	}

	// For capacity: bundle the DSL operator reference alongside the main template.
	if itemType == circulation.ValueCapacity {
		if dslBytes, dslErr := loadTemplateBytes("capacity.dsl.reference.json"); dslErr == nil {
			var dslObj map[string]any
			_ = json.Unmarshal(dslBytes, &dslObj)
			payload["dsl_reference"] = dslObj
			if includeRaw {
				payload["dsl_reference_json"] = string(dslBytes)
			}
		}
	}

	l.emitResponseOK(in, payload)
}
