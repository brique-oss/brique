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

package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestValidChildName(t *testing.T) {
	for _, name := range []string{"a", "child_1", "my-child", "x.y"} {
		if !validChildName(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../x", "a/b", `a\b`, "/abs", " a", "a ", "a\x00b"} {
		if validChildName(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.0.1", "0.0.1", 0},
		{"10.0.0", "9.0.0", 1},
		{"1.9", "1.10", -1},
		{"1.0", "1.0.0", 0},
		{"v2.0.0", "1.99.99", 1},
		{"1.2.3-rc1", "1.2.3", 0},
	}
	for _, tc := range cases {
		got, err := compareVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("compareVersions(%q,%q) = %d,%v want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "x.y", "1..2", "-1.0"} {
		if _, err := compareVersions(bad, "1.0.0"); err == nil {
			t.Errorf("compareVersions(%q) should fail", bad)
		}
	}
}

func TestLoadContextDescriptor_RejectsUnsafeOrDuplicateChildren(t *testing.T) {
	for name, children := range map[string][]any{
		"traversal": {"../outside"},
		"separator": {"a/b"},
		"dotdot":    {".."},
		"duplicate": {"a", "a"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeJSONFile(t, filepath.Join(dir, "context.json"), map[string]any{
				circulation.KeyBrique: map[string]any{
					configuration.KeyContextName:   "c",
					configuration.KeyEngineVersion: shared.EngineBinaryVersion,
					configuration.KeyCtxVersion:    "1",
					configuration.KeyChildList:     children,
				},
			})
			if _, err := LoadContextDescriptor(dir, "/c"); err == nil {
				t.Fatalf("children %v should be rejected", children)
			}
		})
	}
}

func TestControlOneChild_RejectsInvalidName(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	for _, op := range []childOp{childOpStart, childOpStop, childOpRestart} {
		if ok, reason := c.controlOneChild(op, "../x"); ok || reason == "" {
			t.Fatalf("%s with traversal name must fail with a reason", op)
		}
	}
}

// Two concurrent starts of one child must produce exactly one running child.
func TestChildOps_ConcurrentStartCreatesOneChild(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "kid")
	parent := newContextLoopHarness(shared.ContextRunning)
	parent.dir = parentDir
	parent.frame.CtxId = "/parent"
	out := make(chan circulation.Message, 16)
	parent.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: contextDeliveryBridge(out, 16)}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parent.runChildOp(childOpStart, []string{"kid"}, circulation.Intention{IntentionID: "s"})
		}()
	}
	wg.Wait()

	okCount := 0
	for i := 0; i < n; i++ {
		msg := <-out
		if msg.Response.Status == circulation.ValueStatusOK {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("exactly one start must succeed, got %d", okCount)
	}
	parent.childMu.RLock()
	kid := parent.childLoops["kid"]
	parent.childMu.RUnlock()
	if kid == nil || kid.State() != shared.ContextRunning {
		t.Fatalf("kid should be running")
	}
	parent.Stop()
	if kid.State() != shared.ContextStopped {
		t.Fatalf("kid should be stopped with its parent, got %v", kid.State())
	}
}

// Concurrent restarts must leave a single live child, stopped with the parent.
func TestChildOps_ConcurrentRestartLeavesOneChild(t *testing.T) {
	parentDir := t.TempDir()
	writeChildContextFixture(t, parentDir, "kid")
	parent := newContextLoopHarness(shared.ContextRunning)
	parent.dir = parentDir
	parent.frame.CtxId = "/parent"
	out := make(chan circulation.Message, 16)
	parent.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: contextDeliveryBridge(out, 16)}
	if ok, reason := parent.controlOneChild(childOpStart, "kid"); !ok {
		t.Fatalf("initial start: %s", reason)
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parent.runChildOp(childOpRestart, []string{"kid"}, circulation.Intention{IntentionID: "r"})
		}()
	}
	wg.Wait()

	parent.childMu.RLock()
	kid := parent.childLoops["kid"]
	count := len(parent.childLoops)
	parent.childMu.RUnlock()
	if count != 1 || kid == nil || kid.State() != shared.ContextRunning {
		t.Fatalf("expected one running child, got count=%d", count)
	}
	parent.Stop()
	if kid.State() != shared.ContextStopped {
		t.Fatalf("kid should be stopped with its parent")
	}
}

// A child operation racing with Stop never leaves a running child behind.
func TestChildOps_StopRacingStartLeavesNoRunningChild(t *testing.T) {
	for i := 0; i < 10; i++ {
		parentDir := t.TempDir()
		writeChildContextFixture(t, parentDir, "kid")
		parent := newContextLoopHarness(shared.ContextRunning)
		parent.dir = parentDir
		parent.frame.CtxId = "/parent"
		parent.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: make(chan junction.Delivery, 4)}

		done := make(chan struct{})
		go func() {
			defer close(done)
			parent.runChildOp(childOpStart, []string{"kid"}, circulation.Intention{IntentionID: "s"})
		}()
		parent.Stop()
		<-done

		parent.childMu.RLock()
		kid := parent.childLoops["kid"]
		parent.childMu.RUnlock()
		if kid != nil && kid.State() == shared.ContextRunning {
			t.Fatalf("iteration %d: child still running after parent stop", i)
		}
	}
}

func TestRun_UnknownControlCapacityGetsInvalidResponse(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.frame.CtxId = "/parent"
	out := make(chan circulation.Message, 4)
	c.frame.FamIn = junction.FamiliesInChanRegistry{shared.FamilyComm: contextDeliveryBridge(out, 4)}
	c.frame.SetLocalAuthority(contextTestAuthority)
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.run() }()
	defer c.Stop()

	c.ctrl <- contextTestDelivery(circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: "unk-1",
			To:          circulation.Address{Context: "/parent", Cap: "context.explode", Type: circulation.ValueTypeControl},
			From:        circulation.Address{Context: "/caller"},
		},
	})
	select {
	case msg := <-out:
		if msg.Response.Status != circulation.ValueStatusError || msg.Response.Error == nil ||
			msg.Response.Error.Code != circulation.ValueCodeInvalid || msg.Response.IntentionID != "unk-1" {
			t.Fatalf("unexpected response: %#v", msg.Response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response to unknown control capacity")
	}
}

func TestListChildrenNamesIsSorted(t *testing.T) {
	c := newContextLoopHarness(shared.ContextRunning)
	c.installRuntimeAPI()
	for _, n := range []string{"c", "a", "b"} {
		c.childLoops[n] = newContextLoopHarness(shared.ContextRunning)
	}
	got := c.frame.Runtime.ListChildrenNames()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("names = %v", got)
	}
}

func TestParseRootWebSocketListenerConfig_OriginDefaultsAndList(t *testing.T) {
	cfg := parseRootWebSocketListenerConfig(map[string]any{"addr": "127.0.0.1:1"})
	if cfg.AllowAnyOrigin || len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("default must be same-origin only: %#v", cfg)
	}
	if cfg.WrapperAuth != junction.WrapperAuthRequired {
		t.Fatalf("wrapper authentication default = %q, want required", cfg.WrapperAuth)
	}
	cfg = parseRootWebSocketListenerConfig(map[string]any{
		"addr": "127.0.0.1:1", "allowed_origins": []any{" https://ui.example.com ", "", 3},
	})
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "https://ui.example.com" {
		t.Fatalf("allowed origins = %v", cfg.AllowedOrigins)
	}
}

func writeTreeRoot(t *testing.T, dir string, kids []any) {
	t.Helper()
	writeJSONFile(t, filepath.Join(dir, "context.json"), map[string]any{circulation.KeyBrique: map[string]any{
		configuration.KeyContextName: "root", configuration.KeyEngineVersion: shared.EngineBinaryVersion, configuration.KeyCtxVersion: "1",
		configuration.KeyChildList: kids,
		configuration.KeyEngineCfg: map[string]any{
			string(shared.FamilyComm): map[string]any{}, string(shared.FamilyExecution): map[string]any{},
			string(shared.FamilyTrace): map[string]any{}, string(shared.FamilyReflexive): map[string]any{},
		}}})
}

// All children are built concurrently; a broken one does not prevent the
// others and is reported once, in declaration order.
func TestNewContextLoop_BuildsChildrenConcurrentlyAndReportsFailures(t *testing.T) {
	dir := t.TempDir()
	kids := []any{}
	for i := 0; i < 24; i++ {
		name := fmt.Sprintf("k%02d", i)
		if i == 5 || i == 17 {
			_ = os.MkdirAll(filepath.Join(dir, name), 0o755) // no context.json
		} else {
			writeChildContextFixture(t, dir, name)
		}
		kids = append(kids, name)
	}
	writeTreeRoot(t, dir, kids)

	c, err := NewContextLoop(dir, shared.RootContextID, junction.NewContextCommRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	c.childMu.RLock()
	n := len(c.childLoops)
	c.childMu.RUnlock()
	if n != 22 {
		t.Fatalf("children built = %d, want 22", n)
	}
	for _, name := range sortedNames(c.childLoops) {
		if st := c.childLoops[name].State(); st != shared.ContextRunning {
			t.Fatalf("child %s state = %v", name, st)
		}
	}
	c.Stop()
	for _, name := range sortedNames(c.childLoops) {
		if st := c.childLoops[name].State(); st != shared.ContextStopped {
			t.Fatalf("child %s not stopped with parent: %v", name, st)
		}
	}
}
