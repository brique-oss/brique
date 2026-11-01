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

// execution/execution_proc.go
//
// Wrapper OS-process bookkeeping local to the Execution family.
//
// The wrapper runtime state stays in `frame.Wrappers` (junction.WrapperState).
// This file only adds what Execution needs to own the processes it spawns:
// - a handle table so every spawned process is reaped (no zombies) and its
//   exit is observed;
// - one termination path shared by wrapper.stop, wrapper.restart, readiness
//   failure and context shutdown;
// - an emission helper that keeps working while the loop is shutting down.

import (
	"fmt"
	"os/exec"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

const (
	// shutdownEmitTimeout bounds each emission performed while the loop is
	// stopping (Comm may be saturated or already gone).
	shutdownEmitTimeout = time.Second

	// killReapTimeout bounds the wait for a killed process to be reaped.
	killReapTimeout = 2 * time.Second
)

// wrapperProc is the handle of one spawned wrapper process.
type wrapperProc struct {
	pid    int
	cmd    *exec.Cmd
	group  bool          // started in its own process group (pgid == pid)
	exited chan struct{} // closed once the process has been reaped
}

func (l *ExecutionLoop) registerProc(name string, p *wrapperProc) {
	l.procMu.Lock()
	defer l.procMu.Unlock()
	if l.procs == nil {
		l.procs = make(map[string]*wrapperProc)
	}
	l.procs[name] = p
}

// lookupProc returns the handle for (name, pid), or nil when the process was
// not spawned by this loop (or is no longer the current one for name).
func (l *ExecutionLoop) lookupProc(name string, pid int) *wrapperProc {
	l.procMu.Lock()
	defer l.procMu.Unlock()
	if p := l.procs[name]; p != nil && p.pid == pid {
		return p
	}
	return nil
}

func (l *ExecutionLoop) dropProc(name string, p *wrapperProc) {
	l.procMu.Lock()
	defer l.procMu.Unlock()
	if l.procs[name] == p {
		delete(l.procs, name)
	}
}

// watchWrapperProc reaps the process registered for (name, pid) and reflects
// its exit in the wrapper state.
//
// Contract:
//   - must be called once the pid has been recorded in the wrapper state, so the
//     exit handler can recognise the process as the current one;
//   - no-op when no handle is registered for (name, pid).
func (l *ExecutionLoop) watchWrapperProc(name string, pid int) {
	p := l.lookupProc(name, pid)
	if p == nil {
		return
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.exited)
		l.onWrapperExit(name, pid)
		l.dropProc(name, p)
	}()
}

// onWrapperExit applies a process exit to the wrapper state.
//
// Contract:
//   - ignored unless `pid` is still the wrapper's current pid: a replaced or
//     restarted process never alters the state of its successor;
//   - an exit before readiness that was not requested fails readiness (waiters
//     are released immediately instead of waiting for their timeout);
//   - an exit after readiness only marks the wrapper exited; the next intention
//     restarts it through the normal ensureWrapperReady path.
func (l *ExecutionLoop) onWrapperExit(name string, pid int) {
	if l.frame == nil {
		return
	}
	st, ok := l.frame.Wrappers[name]
	if !ok {
		return
	}
	st.Lock()
	defer st.Unlock()
	if st.PID != pid {
		return
	}
	// The process this secret was issued for is gone: nothing may connect with it.
	l.revokeWrapperSecret(name)

	requested := st.ProcState == junction.ProcStopping
	wasReady := st.Ready

	st.ProcState = junction.ProcExited
	st.LastExitAt = time.Now()
	st.PID = 0
	st.Ready = false
	st.ReadyAt = time.Time{}
	st.RunningBuiltAt = time.Time{}
	st.SetStarting(false)

	if !requested {
		err := fmt.Errorf("wrapper %s: process exited unexpectedly", name)
		st.LastError = err
		if !wasReady && st.ReadyErr == nil {
			st.ReadyErr = fmt.Errorf("wrapper %s: process exited before ready", name)
		}
	}
	if ch := st.GetReadyCh(); ch != nil {
		close(ch)
		st.ResetReadyCh()
	}
}

// killWrapperProc kills the process (and its group when it owns one). It does
// not touch the wrapper state: state transitions belong to the exit watcher or
// to the caller.
func (l *ExecutionLoop) killWrapperProc(name string, pid int) {
	if pid <= 0 {
		return
	}
	p := l.lookupProc(name, pid)
	switch {
	case p != nil && p.group:
		killWrapperGroup(pid)
	case p != nil && p.cmd != nil && p.cmd.Process != nil:
		_ = p.cmd.Process.Kill()
	default:
		killWrapperPIDBestEffort(pid)
	}
}

func wrapperProcLive(state junction.WrapperProcState) bool {
	return state == junction.ProcStopping || state == junction.ProcRunning || state == junction.ProcStarting
}

// terminateWrapper
//
// Functional role (Brique DSL):
// - >sequence:
//   - optionally give the process `grace` to exit by itself
//   - verify the process is still the wrapper's current one
//   - kill it (and its group) and make sure the state reflects the exit
//
// Contract:
//   - the single kill path for wrapper.stop, wrapper.restart and shutdown;
//   - a process that was replaced in the meantime is never killed here, and the
//     state of its successor is never modified (stale timers are harmless);
//   - `abortOnStop` makes the grace wait give up when the loop stops; shutdown
//     itself passes false so the kill is not skipped (see stopAllWrappersBestEffort);
//   - without a handle (pid not spawned by this loop) the state is updated here
//     because no watcher will do it.
func (l *ExecutionLoop) terminateWrapper(name string, pid int, grace time.Duration, abortOnStop bool) {
	if pid <= 0 || l.frame == nil {
		return
	}
	p := l.lookupProc(name, pid)

	if grace > 0 {
		t := time.NewTimer(grace)
		defer t.Stop()
		var stopCh <-chan struct{}
		if abortOnStop {
			stopCh = l.done
		}
		var exitedCh <-chan struct{}
		if p != nil {
			exitedCh = p.exited
		}
		select {
		case <-exitedCh:
			return
		case <-stopCh:
			return
		case <-t.C:
		}
	}

	st, ok := l.frame.Wrappers[name]
	if !ok {
		return
	}
	st.Lock()
	current := st.PID == pid && wrapperProcLive(st.ProcState)
	st.Unlock()
	if !current {
		return
	}

	l.killWrapperProc(name, pid)

	if p != nil {
		select {
		case <-p.exited:
		case <-time.After(killReapTimeout):
		}
		return
	}

	// No watcher for this pid: reflect the forced exit directly.
	st.Lock()
	defer st.Unlock()
	if st.PID != pid || !wrapperProcLive(st.ProcState) {
		return
	}
	st.ProcState = junction.ProcExited
	st.LastExitAt = time.Now()
	st.LastError = fmt.Errorf("wrapper %s: killed after stop timeout", name)
	st.PID = 0
	st.Ready = false
	st.SetStarting(false)
	st.SetBuilding(false)
	st.ReadyAt = time.Time{}
	if st.ReadyErr == nil {
		st.ReadyErr = st.LastError
	}
	if ch := st.GetReadyCh(); ch != nil {
		close(ch)
		st.ResetReadyCh()
	}
}

// wrapperStopIntention builds the wrapper_stop signal sent to a wrapper
// boundary through Comm.
func (l *ExecutionLoop) wrapperStopIntention(name string) circulation.Message {
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		Intention: circulation.Intention{
			IntentionID: circulation.NewIntentionID(),
			From: circulation.Address{
				Context: circulation.ContextID(l.frame.CtxId),
				Type:    circulation.ValueTypeExecution,
				Cap:     execCapWrapperStop,
			},
			To: circulation.Address{
				Context: circulation.ContextID("@wrapper_" + name + ":/"),
				Type:    circulation.ValueTypeExecution,
				Cap:     execCapWrapperStop,
			},
			Params: map[string]any{},
		},
	}
}

// emitToCommShutdown emits toward Comm without observing `l.done`.
//
// Contract:
//   - used only for the shutdown sequence, which runs after `done` is closed:
//     `emitToComm` would pick randomly between sending and giving up there;
//   - bounded by shutdownEmitTimeout so a saturated or missing Comm cannot hang
//     shutdown.
func (l *ExecutionLoop) emitToCommShutdown(msg circulation.Message) {
	if l.frame == nil || l.frame.FamIn == nil {
		return
	}
	commCh := l.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	t := time.NewTimer(shutdownEmitTimeout)
	defer t.Stop()
	select {
	case commCh <- l.frame.MustDelivery(msg):
	case <-t.C:
	}
}
