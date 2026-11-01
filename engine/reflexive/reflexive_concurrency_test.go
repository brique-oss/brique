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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func writeParentDescriptor(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, ContextDescriptorFilename)
	if err := os.WriteFile(p, []byte(`{"brique":{"ctx_name":"p","children_list":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Concurrent child creations must all appear in the parent's children_list.
func TestUpdateParentChildListLocked_NoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	desc := writeParentDescriptor(t, dir)
	l := &ReflexiveLoop{editLocks: map[string]*editLock{}}

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.updateParentChildListLocked(dir, fmt.Sprintf("child%02d", i), true); err != nil {
				t.Errorf("child%02d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	var doc struct {
		Brique struct {
			Children []string `json:"children_list"`
		} `json:"brique"`
	}
	b, _ := os.ReadFile(desc)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("descriptor corrupted: %v\n%s", err, b)
	}
	if len(doc.Brique.Children) != n {
		t.Fatalf("lost updates: %d of %d children listed", len(doc.Brique.Children), n)
	}
}

// Idle per-file locks are dropped instead of accumulating.
func TestLockEditPath_ReleasesIdleEntries(t *testing.T) {
	l := &ReflexiveLoop{editLocks: map[string]*editLock{}}
	for i := 0; i < 50; i++ {
		l.lockEditPath(fmt.Sprintf("/f/%d", i))()
	}
	l.editLocksMu.Lock()
	n := len(l.editLocks)
	l.editLocksMu.Unlock()
	if n != 0 {
		t.Fatalf("%d idle locks retained", n)
	}
	// The release function is idempotent.
	unlock := l.lockEditPath("/x")
	unlock()
	unlock()
	l.lockEditPath("/x")()
}

// A content edit (shared) never runs while a structural edit (exclusive) does.
func TestStructuralAndContentEditsExclude(t *testing.T) {
	l := &ReflexiveLoop{editLocks: map[string]*editLock{}}
	releaseStruct := l.lockStructuralEdit()
	acquired := make(chan struct{})
	go func() {
		release := l.lockContentEdit()
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("content edit ran during a structural edit")
	default:
	}
	releaseStruct()
	<-acquired
}

func TestClampSegmentRange(t *testing.T) {
	cases := []struct {
		start, end int
		window     bool
		max        int
		ws, we     int
		trunc      bool
	}{
		{0, 5, false, 10, 0, 5, false},
		{0, 100, false, 10, 90, 100, true}, // newest kept without a window
		{20, 100, true, 10, 20, 30, true},  // window keeps its earliest segments
		{0, 10, false, 10, 0, 10, false},
	}
	for _, c := range cases {
		s, e, tr := clampSegmentRange(c.start, c.end, c.window, c.max)
		if s != c.ws || e != c.we || tr != c.trunc {
			t.Errorf("clamp(%d,%d,%v,%d) = %d,%d,%v want %d,%d,%v", c.start, c.end, c.window, c.max, s, e, tr, c.ws, c.we, c.trunc)
		}
	}
}
