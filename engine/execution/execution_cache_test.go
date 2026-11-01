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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

func cacheTestDSLDoc(name, morphing string) map[string]any {
	return map[string]any{
		"brique": map[string]any{"cap_name": name, "kind": "dsl"},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":    "cache test",
				"inputs":  map[string]any{},
				"outputs": map[string]any{"done": map[string]any{"type": "boolean"}},
				"effects": map[string]any{},
				"transformation_contract": map[string]any{
					"from":      []any{"params"},
					"to":        []any{"done"},
					"affecting": []any{},
					"morphing":  morphing,
				},
				"resolution": map[string]any{"output": map[string]any{"done": true}},
			},
		},
	}
}

// ---- 3.1 wrapper source scans ----

func compiledCacheCfg() map[string]any {
	return map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName: "w1",
				configuration.KeyWrpMode: configuration.ValueConfigStyleCompiled,
				configuration.KeyWrpBuild: map[string]any{
					configuration.KeyWrpCmd: []any{"true"},
				},
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpCmd: []any{"true"},
				},
			},
		},
	}
}

func compiledCacheCfgWithInterval(ms any) map[string]any {
	cfg := compiledCacheCfg()
	w := cfg[configuration.KeyWrappers].([]any)[0].(map[string]any)
	w[configuration.KeyWrpSourceCheckMs] = ms
	return cfg
}

func TestExecutionCache_SourceCheckIntervalParsedFromConfig(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want int
	}{
		{"absent is exact", nil, 0},
		{"positive", int64(250), 250},
		{"zero is exact", int64(0), 0},
		{"negative ignored", int64(-5), 0},
		{"wrong type ignored", "fast", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compiledCacheCfg()
			if tc.raw != nil {
				cfg = compiledCacheCfgWithInterval(tc.raw)
			}
			l, _, _ := newExecutionLoopHarness(cfg)
			w, ok := l.getWrapperCfg("w1")
			if !ok {
				t.Fatalf("wrapper w1 missing")
			}
			if w.SourceCheckIntervalMs != tc.want {
				t.Fatalf("SourceCheckIntervalMs=%d want %d", w.SourceCheckIntervalMs, tc.want)
			}
		})
	}
}

func TestExecutionCache_ReadyWrapperDoesNotRescanSourceEveryCall(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfgWithInterval(int64(60000)))
	var scans atomic.Int64
	l.srcScanFn = func(string) (time.Time, error) {
		scans.Add(1)
		return time.Unix(1, 0), nil
	}

	st := l.frame.Wrappers["w1"]
	now := time.Now()
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.BuiltAt = now
	st.RunningBuiltAt = now
	st.Unlock()

	in := circulation.Intention{IntentionID: "i-cache", Correlation: &circulation.Correlation{}}
	for i := 0; i < 100; i++ {
		if abort := l.ensureWrapperReady("w1", in); abort {
			t.Fatalf("call %d: ready wrapper should not abort", i)
		}
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("source tree scanned %d times for 100 calls, want 1", got)
	}
}

// Default mode is exact: every call observes the current source tree.
func TestExecutionCache_DefaultSourceScanIsExact(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfg())
	var scans atomic.Int64
	l.srcScanFn = func(string) (time.Time, error) {
		scans.Add(1)
		return time.Unix(1, 0), nil
	}
	for i := 0; i < 5; i++ {
		if _, err := l.latestSourceModTime("w1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.Load(); got != 5 {
		t.Fatalf("default mode scanned %d times for 5 calls, want 5 (exact)", got)
	}
}

func TestExecutionCache_SourceChangeSeenAfterInterval(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfg())
	l.srcInterval = 30 * time.Millisecond
	latest := atomic.Pointer[time.Time]{}
	old := time.Unix(1, 0)
	latest.Store(&old)
	l.srcScanFn = func(string) (time.Time, error) { return *latest.Load(), nil }

	builtAt := time.Now()
	got, err := l.latestSourceModTime("w1")
	if err != nil {
		t.Fatalf("latestSourceModTime: %v", err)
	}
	if changed, _ := sourceChangedSince(builtAt, got, nil); changed {
		t.Fatalf("old source must not be reported as changed")
	}

	newer := builtAt.Add(time.Hour)
	latest.Store(&newer)

	// Inside the interval the memoized value is served.
	if got, _ := l.latestSourceModTime("w1"); !got.Equal(old) {
		t.Fatalf("expected memoized value inside the interval, got %v", got)
	}
	time.Sleep(60 * time.Millisecond)
	got, _ = l.latestSourceModTime("w1")
	if changed, _ := sourceChangedSince(builtAt, got, nil); !changed {
		t.Fatalf("source change not detected after the interval")
	}
}

func TestExecutionCache_RealSourceTreeScan(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfg())
	dir := t.TempDir()
	l.frame.ContextDir = dir
	src := filepath.Join(dir, "code", "w1")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	l.srcInterval = 20 * time.Millisecond
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := l.latestSourceModTime("w1")
	if err != nil || first.IsZero() {
		t.Fatalf("first scan: %v %v", first, err)
	}
	future := first.Add(time.Hour)
	b := filepath.Join(src, "b.txt")
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(b, future, future); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	second, _ := l.latestSourceModTime("w1")
	if !second.Equal(future) {
		t.Fatalf("second scan = %v, want %v", second, future)
	}
}

// ---- 3.2 DSL descriptor cache ----

func TestExecutionCache_DSLDescriptorSharedUntilFileChanges(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir

	writeDSLTestCap(t, dir, "cap.dsl.c", cacheTestDSLDoc("cap.dsl.c", "first morphing"))
	d1, err := l.loadDSLDescriptor("cap.dsl.c")
	if err != nil {
		t.Fatalf("load #1: %v", err)
	}
	d2, err := l.loadDSLDescriptor("cap.dsl.c")
	if err != nil {
		t.Fatalf("load #2: %v", err)
	}
	if reflect.ValueOf(d1.RootSection.Resolution).Pointer() != reflect.ValueOf(d2.RootSection.Resolution).Pointer() {
		t.Fatalf("unchanged descriptor should be served from cache")
	}

	writeDSLTestCap(t, dir, "cap.dsl.c", cacheTestDSLDoc("cap.dsl.c", "second morphing, longer"))
	d3, err := l.loadDSLDescriptor("cap.dsl.c")
	if err != nil {
		t.Fatalf("load #3: %v", err)
	}
	if d3.RootSection.TransformationContract.Morphing != "second morphing, longer" {
		t.Fatalf("modified descriptor not reloaded: %q", d3.RootSection.TransformationContract.Morphing)
	}
}

func TestExecutionCache_DSLDescriptorConcurrentLoads(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir
	writeDSLTestCap(t, dir, "cap.dsl.r", cacheTestDSLDoc("cap.dsl.r", "race"))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d, err := l.loadDSLDescriptor("cap.dsl.r")
				if err != nil {
					t.Errorf("load: %v", err)
					return
				}
				// Shared read-only descriptor: evaluate against it.
				st := newDSLExecState(circulation.Intention{IntentionID: "i"}, d.RootSection.Inputs)
				if _, err := evalDSLOutputNode(st, d.RootSection.Resolution["output"].(map[string]any)); err != nil {
					t.Errorf("eval: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// ---- 3.3 capacity lookup ----

func writeCacheCap(t *testing.T, dir, name string) {
	t.Helper()
	writeDSLTestCap(t, dir, name, map[string]any{
		"brique": map[string]any{"cap_name": name, "kind": "dsl"},
	})
}

func TestExecutionCache_UnknownCapacityIsNegativelyCachedUntilDirChanges(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir

	if _, ok := l.getCapEntry("cap.late"); ok {
		t.Fatalf("capacity should be unknown")
	}
	if !l.knownMissing("cap.late") {
		t.Fatalf("absence should be remembered while the directory is unchanged")
	}

	// A descriptor appearing later changes the directory and must be seen at once.
	time.Sleep(10 * time.Millisecond)
	writeCacheCap(t, dir, "cap.late")
	if _, ok := l.getCapEntry("cap.late"); !ok {
		t.Fatalf("capacity created after a miss must be found immediately")
	}
}

func TestExecutionCache_InvalidDescriptorIsNotNegativelyCached(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir
	if err := os.WriteFile(filepath.Join(dir, "cap.bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.getCapEntry("cap.bad"); ok {
		t.Fatalf("invalid descriptor should not resolve")
	}
	if l.knownMissing("cap.bad") {
		t.Fatalf("an unreadable/invalid descriptor must not be cached as absent")
	}
	// Fixing the file in place (directory mtime unchanged) must be picked up.
	writeCacheCap(t, dir, "cap.bad")
	if _, ok := l.getCapEntry("cap.bad"); !ok {
		t.Fatalf("repaired descriptor should resolve")
	}
}

func TestExecutionCache_ConcurrentLookups(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir
	for i := 0; i < 8; i++ {
		writeCacheCap(t, dir, fmt.Sprintf("cap.%d", i))
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, ok := l.getCapEntry(fmt.Sprintf("cap.%d", j%8)); !ok {
					t.Errorf("known capacity not found")
					return
				}
				l.getCapEntry(fmt.Sprintf("missing.%d", j%5))
			}
		}(g)
	}
	wg.Wait()
}

// ---- 3.4 traces ----

func TestExecutionCache_DisabledTraceCostsNothing(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	l.frame.TraceEnabled = false
	allocs := testing.AllocsPerRun(200, func() {
		l.sendToTrace(circulation.ValueKindIntention, "i-1", "p", "r", circulation.ValueTraceFamilyEnter)
	})
	if allocs != 0 {
		t.Fatalf("disabled trace allocated %.1f times per call, want 0", allocs)
	}
}

func TestExecutionCache_EnabledTraceStillEmitted(t *testing.T) {
	l, _, traceCh := newExecutionLoopHarness(nil)
	l.sendToTrace(circulation.ValueKindIntention, "i-2", "", "", circulation.ValueTraceFamilyEnter)
	msg := recvExecMsg(t, traceCh, "trace")
	if msg.Trace.IntentionId != "i-2" || msg.Trace.Timestamp == "" {
		t.Fatalf("unexpected trace: %#v", msg.Trace)
	}
}

func compiledCacheCfgWithWatch(watch ...any) map[string]any {
	cfg := compiledCacheCfg()
	w := cfg[configuration.KeyWrappers].([]any)[0].(map[string]any)
	build, _ := w[configuration.KeyWrpBuild].(map[string]any)
	if build == nil {
		build = map[string]any{}
		w[configuration.KeyWrpBuild] = build
	}
	build[configuration.KeyWrpWatch] = watch
	return cfg
}

func TestExecutionCache_WatchedLibraryMakesBuildStale(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfgWithWatch("../../../wrapper/go/lib", "  "))
	srcDir, _ := l.wrapperSourceDir("w1")
	libDir := filepath.Join(srcDir, "../../../wrapper/go/lib")
	var mu sync.Mutex
	times := map[string]time.Time{srcDir: time.Unix(10, 0), libDir: time.Unix(5, 0)}
	var scanned []string
	l.srcScanFn = func(dir string) (time.Time, error) {
		mu.Lock()
		defer mu.Unlock()
		scanned = append(scanned, dir)
		return times[dir], nil
	}

	got, err := l.latestSourceModTime("w1")
	if err != nil || !got.Equal(time.Unix(10, 0)) {
		t.Fatalf("latest=%v err=%v, want the wrapper tree time", got, err)
	}
	if len(scanned) != 2 || scanned[1] != libDir {
		t.Fatalf("scanned %v, want wrapper dir then resolved library dir (blank entry ignored)", scanned)
	}

	mu.Lock()
	times[libDir] = time.Unix(99, 0)
	mu.Unlock()
	got, _ = l.latestSourceModTime("w1")
	if changed, _ := sourceChangedSince(time.Unix(50, 0), got, nil); !changed {
		t.Fatalf("a newer library file must make the compiled wrapper stale")
	}
}

func TestExecutionCache_WatchedLibraryScanErrorIsReported(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfgWithWatch("missing"))
	l.srcScanFn = func(dir string) (time.Time, error) {
		if strings.HasSuffix(dir, "missing") {
			return time.Time{}, os.ErrNotExist
		}
		return time.Unix(1, 0), nil
	}
	if _, err := l.latestSourceModTime("w1"); err == nil || !strings.Contains(err.Error(), "watch") {
		t.Fatalf("err=%v, want a watch error", err)
	}
}

func interpretedCfg(extra map[string]any) map[string]any {
	w := map[string]any{
		configuration.KeyWrpName: "w1",
		configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
		configuration.KeyWrpRun: map[string]any{
			configuration.KeyWrpCmd: []any{"sleep", "30"},
		},
		configuration.KeyWrpReadyTO: int64(50),
	}
	for k, v := range extra {
		w[k] = v
	}
	return map[string]any{configuration.KeyWrappers: []any{w}}
}

func readyInterpreted(t *testing.T, l *ExecutionLoop, startedSource time.Time) *junction.WrapperState {
	t.Helper()
	l.frame.ContextDir = t.TempDir()
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.PID = 0
	st.RunningSourceAt = startedSource
	st.Unlock()
	return st
}

func TestExecutionInterpreted_RuntimeStaleTable(t *testing.T) {
	t0 := time.Unix(100, 0)
	running := func(started time.Time) *junction.WrapperState {
		st := &junction.WrapperState{ProcState: junction.ProcRunning, Ready: true, RunningSourceAt: started}
		return st
	}
	cases := []struct {
		name   string
		st     *junction.WrapperState
		latest time.Time
		want   bool
	}{
		{"nil state", nil, t0, false},
		{"newer source", running(t0), t0.Add(time.Second), true},
		{"same time", running(t0), t0, false},
		{"older source", running(t0), t0.Add(-time.Second), false},
		{"no scan result", running(t0), time.Time{}, false},
		{"no start reference", running(time.Time{}), t0, false},
		{"not running", &junction.WrapperState{ProcState: junction.ProcExited, Ready: true, RunningSourceAt: t0}, t0.Add(time.Second), false},
		{"not ready", &junction.WrapperState{ProcState: junction.ProcRunning, RunningSourceAt: t0}, t0.Add(time.Second), false},
	}
	for _, tc := range cases {
		if got := interpretedRuntimeStaleLocked(tc.st, tc.latest); got != tc.want {
			t.Errorf("%s: stale=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestExecutionInterpreted_UnchangedSourceKeepsRunningProcess(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(interpretedCfg(nil))
	t0 := time.Unix(100, 0)
	l.srcInterval = time.Nanosecond // detection on, memoization effectively off
	l.srcScanFn = func(string) (time.Time, error) { return t0, nil }
	readyInterpreted(t, l, t0)

	in := circulation.Intention{IntentionID: "i-keep", Correlation: &circulation.Correlation{}}
	for i := 0; i < 3; i++ {
		if abort := l.ensureWrapperReady("w1", in); abort {
			t.Fatalf("call %d: unchanged source must keep the ready wrapper", i)
		}
	}
}

func TestExecutionInterpreted_NewerSourceRestartsProcess(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(interpretedCfg(nil))
	t0 := time.Unix(100, 0)
	var newer atomic.Bool
	l.srcInterval = time.Nanosecond // detection on, memoization effectively off
	l.srcScanFn = func(string) (time.Time, error) {
		if newer.Load() {
			return t0.Add(time.Hour), nil
		}
		return t0, nil
	}
	st := readyInterpreted(t, l, t0)

	in := circulation.Intention{IntentionID: "i-restart", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); abort {
		t.Fatalf("unchanged source must not abort")
	}

	newer.Store(true)
	// The restart spawns the real run command; it never reports ready, so the
	// call ends with a readiness timeout. That proves the fast path was left
	// and a new process was started (and then cleaned up).
	abort := l.ensureWrapperReady("w1", in)
	if !abort {
		t.Fatalf("a newer source must leave the fast path and restart the wrapper")
	}
	st.Lock()
	defer st.Unlock()
	if !st.RunningSourceAt.IsZero() {
		t.Fatalf("a failed restart must not record a running source time, got %v", st.RunningSourceAt)
	}
}

func TestExecutionInterpreted_PycacheDoesNotCountAsEdit(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.py")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1000, 0)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "__pycache__")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "main.cpython-311.pyc"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.pyc"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := latestModTimeInTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(old) {
		t.Fatalf("latest=%v, want %v: bytecode caches must be ignored", got, old)
	}
}

func TestExecutionInterpreted_WrapperLevelWatchParsedAndScanned(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(interpretedCfg(map[string]any{
		configuration.KeyWrpWatch: []any{"../lib"},
		configuration.KeyWrpBuild: map[string]any{configuration.KeyWrpWatch: []any{"../other"}},
	}))
	cfg, ok := l.getWrapperCfg("w1")
	if !ok || len(cfg.Watch) != 1 || cfg.Watch[0] != "../lib" {
		t.Fatalf("wrapper-level watch not parsed: %#v", cfg.Watch)
	}
	var scanned []string
	l.srcScanFn = func(dir string) (time.Time, error) {
		scanned = append(scanned, filepath.Base(dir))
		return time.Unix(1, 0), nil
	}
	if _, err := l.latestSourceModTime("w1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scanned, []string{"w1", "lib", "other"}) {
		t.Fatalf("scanned %v, want wrapper dir then both watch lists", scanned)
	}
}

func TestExecutionSourceCheck_DisabledByDefault(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"interpreted": interpretedCfg(nil),
		"compiled":    compiledCacheCfg(),
	} {
		t.Run(name, func(t *testing.T) {
			l, _, _ := newExecutionLoopHarness(cfg)
			var scans atomic.Int64
			l.srcScanFn = func(string) (time.Time, error) {
				scans.Add(1)
				return time.Unix(1<<30, 0), nil
			}
			wcfg, _ := l.getWrapperCfg("w1")
			if l.sourceCheckEnabled(wcfg) {
				t.Fatalf("source check must be off when no interval is configured")
			}
			if name == "interpreted" {
				readyInterpreted(t, l, time.Unix(100, 0))
				in := circulation.Intention{IntentionID: "i-off", Correlation: &circulation.Correlation{}}
				if abort := l.ensureWrapperReady("w1", in); abort {
					t.Fatalf("a ready wrapper must stay ready when the check is off")
				}
			}
			if got := scans.Load(); got != 0 {
				t.Fatalf("source tree scanned %d times, want 0 when the check is off", got)
			}
		})
	}
}

func TestExecutionSourceCheck_EnabledByPositiveInterval(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(compiledCacheCfgWithInterval(int64(250)))
	wcfg, _ := l.getWrapperCfg("w1")
	if !l.sourceCheckEnabled(wcfg) {
		t.Fatalf("a positive interval must enable the check")
	}
}
