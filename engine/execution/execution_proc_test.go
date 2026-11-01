//go:build !windows

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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func procTestCfg(run []any, shell bool, readyMs, stopMs int) map[string]any {
	return map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName:    "w1",
				configuration.KeyWrpMode:    configuration.ValueConfigStyleInterpreted,
				configuration.KeyWrpReadyTO: readyMs,
				configuration.KeyWrpStopTO:  stopMs,
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpCmd:   run,
					configuration.KeyWrpShell: shell,
				},
			},
		},
	}
}

// newProcTestLoop builds a loop whose context dir exists on disk so wrapper
// processes can really be spawned.
func newProcTestLoop(t *testing.T, cfg map[string]any) (*ExecutionLoop, chan circulation.Message, string) {
	t.Helper()
	l, commCh, _ := newExecutionLoopHarness(cfg)
	dir := t.TempDir()
	l.frame.ContextDir = dir
	if err := os.MkdirAll(filepath.Join(dir, shared.ContextDirCode, "w1"), 0o755); err != nil {
		t.Fatalf("mkdir wrapper source dir: %v", err)
	}
	t.Cleanup(func() {
		// Never leave a test process behind.
		if st := l.frame.Wrappers["w1"]; st != nil {
			st.Lock()
			pid := st.PID
			st.Unlock()
			l.killWrapperProc("w1", pid)
		}
	})
	return l, commCh, dir
}

// spawnTracked starts the configured wrapper process the way ensureWrapperReady
// does and records it in the wrapper state.
func spawnTracked(t *testing.T, l *ExecutionLoop, ready bool) int {
	t.Helper()
	cfg, ok := l.getWrapperCfg("w1")
	if !ok {
		t.Fatalf("wrapper w1 not configured")
	}
	pid, err := l.startWrapperProcess(cfg)
	if err != nil {
		t.Fatalf("startWrapperProcess: %v", err)
	}
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.PID = pid
	st.ProcState = junction.ProcRunning
	st.Ready = ready
	if !ready {
		st.SetReadyCh()
	}
	l.watchWrapperProc("w1", pid)
	st.Unlock()
	return pid
}

func pidAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func waitProc(t *testing.T, timeout time.Duration, pred func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", label)
}

// B2: the wrapper stop intention must be emitted by Stop() every time, even
// though the loop's done channel is already closed at that point.
func TestExecutionProc_StopAlwaysEmitsWrapperStop(t *testing.T) {
	for round := 0; round < 30; round++ {
		l, commCh, _ := newExecutionLoopHarness(procTestCfg([]any{"sleep", "30"}, false, 0, 0))
		l.Start()
		l.Stop()

		// The test bridge between Comm's channel and commCh is asynchronous:
		// wait for the stop intention instead of draining once.
		found := false
		timeout := time.After(2 * time.Second)
	wait:
		for !found {
			select {
			case m := <-commCh:
				found = m.Kind == circulation.ValueKindIntention && m.Intention.To.Cap == execCapWrapperStop
			case <-timeout:
				break wait
			}
		}
		if !found {
			t.Fatalf("round %d: Stop() did not emit wrapper stop intention", round)
		}
	}
}

// B2: shutdown must really terminate a wrapper that ignores the stop signal.
func TestExecutionProc_StopKillsWrapperAfterTimeout(t *testing.T) {
	l, _, _ := newProcTestLoop(t, procTestCfg([]any{"sleep", "30"}, false, 0, 300))
	l.Start()
	pid := spawnTracked(t, l, true)

	started := time.Now()
	l.Stop()
	elapsed := time.Since(started)

	waitProc(t, 2*time.Second, func() bool { return !pidAlive(pid) }, "wrapper process killed by Stop")
	if elapsed > 3*time.Second {
		t.Fatalf("Stop took too long: %v", elapsed)
	}
	st := l.frame.Wrappers["w1"]
	waitProc(t, time.Second, func() bool {
		st.Lock()
		defer st.Unlock()
		return st.PID == 0 && st.ProcState == junction.ProcExited
	}, "state reflects exit")
}

// B3: a stale kill timer must neither kill nor modify a successor process.
func TestExecutionProc_StaleKillTimerLeavesSuccessorUntouched(t *testing.T) {
	l, _, _ := newProcTestLoop(t, procTestCfg([]any{"sleep", "30"}, false, 0, 0))
	oldPID := spawnTracked(t, l, true)
	newPID := spawnTracked(t, l, true) // replaces old as the wrapper's process

	l.terminateWrapper("w1", oldPID, 20*time.Millisecond, false)

	st := l.frame.Wrappers["w1"]
	st.Lock()
	pid, state, ready := st.PID, st.ProcState, st.Ready
	st.Unlock()
	if pid != newPID || state != junction.ProcRunning || !ready {
		t.Fatalf("successor state altered: pid=%d (want %d) state=%v ready=%v", pid, newPID, state, ready)
	}
	if !pidAlive(newPID) {
		t.Fatalf("successor process was killed")
	}
	l.killWrapperProc("w1", oldPID)
}

// B4: a wrapper that never becomes ready must not be left running.
func TestExecutionProc_ReadinessTimeoutKillsProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	l, _, _ := newProcTestLoop(t, procTestCfg(
		[]any{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 30"}, false, 150, 0))

	abort := l.ensureWrapperReady("w1", circulation.Intention{IntentionID: "i-timeout", Correlation: &circulation.Correlation{}})
	if !abort {
		t.Fatalf("readiness timeout should abort")
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	waitProc(t, 2*time.Second, func() bool { return !pidAlive(pid) }, "unready wrapper process killed")

	st := l.frame.Wrappers["w1"]
	waitProc(t, time.Second, func() bool {
		st.Lock()
		defer st.Unlock()
		return st.PID == 0
	}, "pid cleared")
}

// A wrapper that exits after being ready is marked exited (and will be
// restarted by the next intention), without a readiness error.
func TestExecutionProc_ExitAfterReadyMarksExited(t *testing.T) {
	l, _, _ := newProcTestLoop(t, procTestCfg([]any{"sleep", "0.2"}, false, 0, 0))
	spawnTracked(t, l, true)

	st := l.frame.Wrappers["w1"]
	waitProc(t, 3*time.Second, func() bool {
		st.Lock()
		defer st.Unlock()
		return st.ProcState == junction.ProcExited
	}, "wrapper marked exited")

	st.Lock()
	defer st.Unlock()
	if st.PID != 0 || st.Ready || st.ReadyErr != nil {
		t.Fatalf("unexpected state after exit: pid=%d ready=%v readyErr=%v", st.PID, st.Ready, st.ReadyErr)
	}
}

// A wrapper that exits before being ready releases readiness waiters at once
// and fails readiness.
func TestExecutionProc_ExitBeforeReadyFailsReadiness(t *testing.T) {
	l, _, _ := newProcTestLoop(t, procTestCfg([]any{"sleep", "0.1"}, false, 0, 0))
	st := l.frame.Wrappers["w1"]
	spawnTracked(t, l, false) // installs the ready channel
	st.Lock()
	ch := st.GetReadyCh()
	st.Unlock()

	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("readiness waiters were not released by process exit")
	}
	st.Lock()
	defer st.Unlock()
	if st.ReadyErr == nil || st.Ready || st.PID != 0 {
		t.Fatalf("expected failed readiness after early exit: ready=%v err=%v pid=%d", st.Ready, st.ReadyErr, st.PID)
	}
}

// Termination of a shell-run wrapper must take its children with it.
func TestExecutionProc_ShellWrapperKillTakesChildren(t *testing.T) {
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child")
	l, _, _ := newProcTestLoop(t, procTestCfg(
		[]any{"sleep 30 & echo $! > " + childFile + "; wait"}, true, 0, 0))
	pid := spawnTracked(t, l, true)

	var child int
	waitProc(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(childFile)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		child = n
		return err == nil && n > 0
	}, "child pid written")
	if !pidAlive(child) {
		t.Fatalf("child should be running before termination")
	}

	l.terminateWrapper("w1", pid, 0, false)

	waitProc(t, 3*time.Second, func() bool { return !pidAlive(child) }, "shell child killed with its group")
}
