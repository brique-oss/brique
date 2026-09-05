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

package shared_test

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/shared"
)

// Covers: N1-AR-02
func TestAtomicReplace_N1_AR_02_EmptyPath_ReturnsError(t *testing.T) {
	if err := shared.AtomicReplace("", "/tmp/dst"); err == nil {
		t.Fatalf("expected error for empty tmp path")
	}
	if err := shared.AtomicReplace("/tmp/tmp", ""); err == nil {
		t.Fatalf("expected error for empty dst path")
	}
}

// Covers: N1-AR-03
func TestAtomicReplace_N1_AR_03_MissingTmp_ReturnsErrorAndKeepsDst(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	missingTmp := filepath.Join(dir, "missing.tmp")
	err := shared.AtomicReplace(missingTmp, dst)
	if err == nil {
		t.Fatalf("expected error for missing tmp")
	}

	content, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("read dst: %v", readErr)
	}
	if string(content) != "old" {
		t.Fatalf("dst modified on failed replace: got %q, want %q", string(content), "old")
	}
}

// Covers: N1-AR-04
func TestAtomicReplace_N1_AR_04_InvalidDst_ReturnsErrorAndKeepsTmp(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp.txt")
	if err := os.WriteFile(tmp, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	invalidDst := filepath.Join(dir, "missing-dir", "dst.txt")
	err := shared.AtomicReplace(tmp, invalidDst)
	if err == nil {
		t.Fatalf("expected error for invalid dst path")
	}

	if _, statErr := os.Stat(tmp); statErr != nil {
		t.Fatalf("tmp should remain after failed replace, stat err: %v", statErr)
	}
}

// Covers: N1-AR-01, N1-AR-05
func TestAtomicReplace_N1_AR_01_05_Success_ReplacesExistingDstAndConsumesTmp(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := shared.AtomicReplace(tmp, dst); err != nil {
		t.Fatalf("AtomicReplace returned error: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("dst content = %q, want %q", string(content), "new")
	}

	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("tmp should be consumed after success, stat err: %v", err)
	}
}
