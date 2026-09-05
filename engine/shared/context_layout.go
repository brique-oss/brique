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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Canonical context-root namespaces.
//
// These names define the reserved top-level layout used by a Brique context on
// disk. They are relative to the owning context directory.
const (
	ContextDirCapacity  = "capacity"
	ContextDirDocument  = "document"
	ContextDirSchema    = "schema"
	ContextDirMatter    = "matter"
	ContextDirStructure = "structure"

	ContextDirCode  = "code"
	ContextDirBuild = "build"
	ContextDirTmp   = "tmp"

	ContextDescriptorFilename = "context.json"
)

// EnsureWithinDir
//
// Inputs:
// - rootDir, targetPath string.
//
// Outputs:
// - returns (bool, error).
//
// Contract:
// - Returns true when targetPath is rootDir itself or a descendant of it.
// - Reports scope-validation failure via the returned error.
func EnsureWithinDir(rootDir, targetPath string) (bool, error) {
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
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false, nil
	}
	return true, nil
}

// ResolveDestinationContextDir
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
// - destCtxID must be an absolute path rooted at RootContextID.
// - Reports failure via returned error; callers convert it to the
//   appropriate circulation response semantics.
func ResolveDestinationContextDir(selfCtxID, selfCtxDir, destCtxID string) (string, error) {
	destCtxID = strings.TrimSpace(destCtxID)
	if !strings.HasPrefix(destCtxID, RootContextID) {
		return "", fmt.Errorf("destination_ctx_id must be an absolute path starting with %q", RootContextID)
	}

	selfCtxID = strings.TrimSpace(selfCtxID)
	selfRel := strings.TrimPrefix(selfCtxID, RootContextID)
	rootDir := strings.TrimSuffix(selfCtxDir, filepath.FromSlash(selfRel))

	destRel := strings.TrimPrefix(strings.TrimPrefix(destCtxID, RootContextID), "/")
	destDir := filepath.Join(rootDir, filepath.FromSlash(destRel))

	if ok, err := EnsureWithinDir(rootDir, destDir); err != nil || !ok {
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
