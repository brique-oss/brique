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

package junction

import (
	"sync/atomic"
	"testing"
)

func TestProjectionNotifier_CoalescingListenerAndNilSafety(t *testing.T) {
	var nilReg *ContextCommRegistry
	nilReg.NotifyProjectionChange() // must not panic
	nilReg.SetProjectionNotifier(func(...ProjectionHint) {})

	r := NewContextCommRegistry()
	r.NotifyProjectionChange() // no listener: no-op

	var n atomic.Int32
	var got atomic.Value
	r.SetProjectionNotifier(func(h ...ProjectionHint) { n.Add(1); got.Store(h) })
	r.NotifyProjectionChange()
	r.NotifyProjectionChange(ProjectionHint{CtxID: "/root", ElementType: "matter", Name: "m1"})
	if n.Load() != 2 {
		t.Fatalf("listener called %d times, want 2", n.Load())
	}
	if h := got.Load().([]ProjectionHint); len(h) != 1 || h[0].Name != "m1" {
		t.Fatalf("hints not forwarded: %v", h)
	}

	r.SetProjectionNotifier(nil)
	r.NotifyProjectionChange()
	if n.Load() != 2 {
		t.Fatal("listener still called after removal")
	}

	var _ ProjectionNotifier = r
	var _ ProjectionNotifierRegistrar = r
}
