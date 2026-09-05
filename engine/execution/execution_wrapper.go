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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
	"time"
)

// ensureWrapperReady
//
// Functional role (Brique DSL):
// - >sequence:
//   - resolve wrapper config and wrapper runtime state
//   - fail fast on missing config, invalid mode, or prior readiness failure
//   - decide whether build, start, or wait is required
//   - for compiled wrappers: run build once when needed
//   - start wrapper process when not running
//   - wait for ready signal or timeout/stop
//   - emit traced error responses on build/start/readiness failure
//
//
// Expected Message Fields:
// - intention fields consumed directly or indirectly:
//   - `intention`
//   - `intention.intentionid`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//   - `intention.from`
//   - `intention.to`
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - response.intentionid via `errorResp`.
//   - response.to via `errorResp`.
//   - response.from via `errorResp`.
//   - response.status via `errorResp`.
//   - response.error.origin via `errorResp`.
//   - response.error.code via `errorResp`.
//   - response.error.message via `errorResp`.
//   - response.error.details via `errorResp`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - `circulation.KeyReason`
//   - `mode`
//
// Produced Trace:
// - Valid:
//   - emits wrapper lifecycle traces for build/start/wait progress when those phases are entered.
// - On error:
//   - emits wrapper lifecycle traces up to the failing phase and family-error traces for readiness/build/start failures.
//
// Produced Outbound Message:
// - Valid:
//   - emits wrapper lifecycle trace messages during build/start/wait orchestration.
// - On error:
//   - emits one wrapper readiness error response to Comm plus the associated trace traffic.
//
// State/Storage Effects:
// - reads and mutates wrapper runtime state in the shared frame.
// - may build and start wrapper processes.
// - may wait on the wrapper ready channel.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - wrapper string, in circulation.Intention.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Returns `false` only when wrapper is ready to use; all failure paths emit an error response before returning `true`.
// - A build/start failure records readiness failure in wrapper state and unblocks current waiters best effort.
// - Already-ready wrappers return immediately without rebuilding or restarting.

func (l *ExecutionLoop) ensureWrapperReady(wrapper string, in circulation.Intention) bool {
	parentID, rootID := executionCorrelationIDs(in)
	cfg, ok := l.getWrapperCfg(wrapper)
	if !ok || cfg.Name == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured},
			"wrapper not configured in execution config"))
		return true
	}

	// 1) Snapshot state (or create) + decide what to do
	st, ok := l.frame.Wrappers[wrapper]
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured}, "wrapper not configured"))
		return true
	}
	st.Lock()

	// Decide what must be done (explicit build/start split)
	isCompiled := (cfg.Mode == configuration.ValueConfigStyleCompiled)
	isInterpreted := (cfg.Mode == configuration.ValueConfigStyleInterpreted)

	// Invalid mode => configuration error (fail fast)
	if !isCompiled && !isInterpreted {
		st.Unlock()
		l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured, "mode": cfg.Mode},
			"invalid wrapper mode"))
		return true
	}

	sourceChanged := false
	if isCompiled && cfg.Build != nil {
		changed, err := l.wrapperSourceNewerThanBuild(cfg.Name, st.BuiltAt)
		if err != nil && st.BuiltAt.IsZero() {
			st.Unlock()
			l.emitResponseError(errorResp(in, circulation.ValueCodeConfiguration,
				map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotConfigured},
				err.Error()))
			return true
		}
		sourceChanged = changed
	}

	runningStale := isCompiled && wrapperRuntimeStaleLocked(st)

	// Fast path: already ready (current running process) and not stale.
	if st.ProcState == junction.ProcRunning && st.Ready && st.ReadyErr == nil && !sourceChanged && !runningStale && !st.GetBuilding() && !st.GetStarting() {
		st.Unlock()
		return false
	}

	// If readiness already failed for current process, fail-fast
	if st.ReadyErr != nil {
		st.Unlock()
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperFailedReady}, "wrapper failed to be ready"))
		return true
	}

	needsBuild := (isCompiled && cfg.Build != nil)
	buildToDo := needsBuild && (st.BuiltAt.IsZero() || sourceChanged)
	needBuildNow := buildToDo && !st.GetBuilding()

	// If we will either build or start, we must have a readiness channel to synchronize waiters.
	if needBuildNow && st.GetReadyCh() == nil {
		st.SetReadyCh()
	}

	// 2) Build (compiled only, single in-flight)
	if needBuildNow {
		st.SetBuilding(true)
		l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrpStartBuilding)
		// do build outside lock
		st.Unlock()
		if err := l.buildWrapper(cfg); err != nil {
			st.Lock()
			st.SetBuilding(false)
			// Unblock any waiters: build failure means readiness failed.
			st.ReadyErr = err
			ch := st.GetReadyCh()
			if ch != nil {
				close(ch)
				st.ResetReadyCh()
			}
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperBuildFailed}, err.Error()))
			st.Unlock()
			return true
		}
		l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrpEndBuilding)
		st.Lock()
		st.BuiltAt = time.Now()
		st.SetBuilding(false)
	}

	// Compiled runtime is stale when a newer build exists than the currently running generation.
	if isCompiled && wrapperRuntimeStaleLocked(st) && st.ProcState == junction.ProcRunning && st.Ready && st.ReadyErr == nil {
		pid := markWrapperRestartingLocked(st)
		st.Unlock()
		killWrapperPIDBestEffort(pid)
		st.Lock()
	}

	// startToDo:
	// - interpreted: start whenever not running
	// - compiled: start whenever not running AND already built
	startToDo := false
	switch {
	case isInterpreted:
		startToDo = st.ProcState != junction.ProcRunning
	case isCompiled:
		startToDo = st.ProcState != junction.ProcRunning && !st.BuiltAt.IsZero()
	}

	needStart := st.ProcState != junction.ProcRunning && startToDo && !st.GetStarting()
	if needStart && st.GetReadyCh() == nil {
		st.SetReadyCh()
	}

	// 3) Start when not running.
	// IMPORTANT:
	// - compiled: if buildToDo was true, only the builder reaches here with startToDo true while holding the lock,
	//   because it sets st.starting before unlocking.
	// - compiled already built: start is allowed even if buildToDo is false.
	if needStart {
		l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrpStartRequested)
		st.SetStarting(true)
		st.Ready = false
		st.ReadyAt = time.Time{}
		st.ReadyErr = nil
		st.ProcState = junction.ProcStarting
		st.StartedAt = time.Now()

		st.Unlock()
		pid, err := l.startWrapperProcess(cfg)
		if err != nil {
			st.Lock()
			st.ProcState = junction.ProcUnknown
			st.SetStarting(false)
			st.LastError = err
			st.ReadyErr = err
			ch := st.GetReadyCh()
			if ch != nil {
				close(ch)
				st.ResetReadyCh()
			}
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperRunFailed}, err.Error()))
			st.Unlock()
			return true
		}

		// Mark running early so wrapper_ready can unlock the waiter.
		st.Lock()
		st.PID = pid
		st.ProcState = junction.ProcRunning
		ch := st.GetReadyCh()
		st.Unlock()

		if ch == nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
				"internal error: wrapper started without ready channel"))
			return true
		}
		if err := l.waitWrapperReady(ch, cfg.ReadyTimeoutMs); err != nil {
			st.Lock()
			st.SetStarting(false)
			st.ProcState = junction.ProcUnknown
			ch := st.GetReadyCh()
			if ch != nil {
				close(ch)
				st.ResetReadyCh()
			}
			if err.Error() == "stopped" {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonContextStopped}, "Context has been stopped"))
				st.ReadyErr = fmt.Errorf("wrapper context stopped")
			} else {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperTimeout}, "wrapper readiness timeout"))
				st.ReadyErr = fmt.Errorf("wrapper readiness timeout")
			}
			st.Unlock()
			return true
		}

		l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrpStartDone)
		st.Lock()
		st.SetStarting(false)
		if isCompiled {
			st.RunningBuiltAt = st.BuiltAt
		}

		if st.ReadyErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperFailedReady}, st.ReadyErr.Error()))
			st.Unlock()
			return true
		}
		st.Unlock()
		return false

	} else {
		// wrapper not ready yet -> just wait
		l.sendToTrace(circulation.ValueKindIntention, in.IntentionID, parentID, rootID, circulation.ValueTraceWrpWaitStart)
		ch := st.GetReadyCh()
		if ch == nil {
			// Invariant violation: another job started, but no ready channel to wait on.
			// Do not create a new channel here (it would never be closed).
			st.Unlock()
			l.emitResponseError(errorResp(in, circulation.ValueCodeInternal,
				map[string]any{circulation.KeyReason: circulation.ValueReasonAlgoFailure},
				"internal error: wrapper starting without ready channel"))
			return true
		}
		st.Unlock()
		if err := l.waitWrapperReady(ch, cfg.ReadyTimeoutMs); err != nil {
			if err.Error() == "stopped" {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonContextStopped}, "Context has been stopped"))
			} else {
				l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperTimeout}, "wrapper readiness timeout"))
			}
			return true
		}
		st.Lock()
		if st.ReadyErr != nil {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperFailedReady}, st.ReadyErr.Error()))
			st.Unlock()
			return true
		}
		if st.ProcState == junction.ProcRunning && st.Ready {
			st.Unlock()
			return false
		}
		st.Unlock()
		return true
	}
}

func (l *ExecutionLoop) wrapperSourceNewerThanBuild(wrapper string, builtAt time.Time) (bool, error) {
	if builtAt.IsZero() {
		return true, nil
	}
	sourceDir, ok := l.wrapperSourceDir(wrapper)
	if !ok {
		return false, fmt.Errorf("wrapper %s: missing context dir", wrapper)
	}
	latest, err := latestModTimeInTree(sourceDir)
	if err != nil {
		return false, err
	}
	if latest.IsZero() {
		return false, nil
	}
	return latest.After(builtAt), nil
}

func latestModTimeInTree(root string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mod := info.ModTime()
		if mod.After(latest) {
			latest = mod
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

func wrapperRuntimeStaleLocked(st *junction.WrapperState) bool {
	if st == nil {
		return false
	}
	if st.ProcState != junction.ProcRunning || !st.Ready || st.ReadyErr != nil {
		return false
	}
	if st.BuiltAt.IsZero() {
		return false
	}
	if st.RunningBuiltAt.IsZero() {
		return true
	}
	return st.RunningBuiltAt.Before(st.BuiltAt)
}

func markWrapperRestartingLocked(st *junction.WrapperState) int {
	if st == nil {
		return 0
	}
	pid := st.PID
	st.ProcState = junction.ProcExited
	st.LastExitAt = time.Now()
	st.PID = 0
	st.Ready = false
	st.ReadyAt = time.Time{}
	st.ReadyErr = nil
	st.RunningBuiltAt = time.Time{}
	st.SetStarting(false)
	ch := st.GetReadyCh()
	if ch != nil {
		close(ch)
		st.ResetReadyCh()
	}
	return pid
}

func killWrapperPIDBestEffort(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil || p == nil {
		return
	}
	_ = p.Kill()
}

// buildWrapper
//
// Functional role (Brique DSL):
// - execute configured wrapper build command with optional shell mode and timeout.
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
// - derives wrapper source/build/work directories from the owning context dir.
// - creates build/work directories best effort before process start.
// - starts and waits on the configured build process.
// - streams build stdout/stderr to the current process.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - cfg WrapperRuntimeCfg.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Timeout applies only when `Build.TimeoutMs > 0`.
// - Wrapper build cwd is always derived as `<ctxDir>/code/<wrapper_name>`.
// - Wrapper build output directory is always derived as `<ctxDir>/build/<wrapper_name>`.
// - Wrapper private work directory is always derived as `<ctxDir>/tmp/wrapper_<wrapper_name>`.
// - Missing build config, empty command, empty wrapper name, or missing context dir fail before any process is started.

func (l *ExecutionLoop) buildWrapper(cfg WrapperRuntimeCfg) error {
	if cfg.Build == nil {
		return fmt.Errorf("wrapper %s: missing build config", cfg.Name)
	}
	b := cfg.Build
	if len(b.Cmd) == 0 {
		return fmt.Errorf("wrapper %s: empty build cmd", cfg.Name)
	}
	sourceDir, ok := l.wrapperSourceDir(cfg.Name)
	if !ok {
		return fmt.Errorf("wrapper %s: missing context dir", cfg.Name)
	}
	buildDir, _ := l.wrapperBuildDir(cfg.Name)
	workDir, _ := l.wrapperWorkDir(cfg.Name)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return fmt.Errorf("wrapper %s: prepare build dir: %w", cfg.Name, err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("wrapper %s: prepare work dir: %w", cfg.Name, err)
	}

	// Timeout: if TimeoutMs > 0, cancel the build process.
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if b.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(b.TimeoutMs)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	buildCmd := b.Cmd
	if cfg.Mode == configuration.ValueConfigStyleCompiled {
		buildCmd = applyCompiledWrapperExeExt(buildCmd, false)
	}

	var cmd *exec.Cmd
	if b.Shell {
		// Build.Cmd is a shell command line
		s := strings.Join(buildCmd, " ")
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd.exe", "/C", s)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-lc", s)
		}
	} else {
		cmd = exec.CommandContext(ctx, buildCmd[0], buildCmd[1:]...)
	}
	cmd.Dir = sourceDir
	cmd.Env = mergeEnv(os.Environ(), l.wrapperRuntimeEnv(cfg.Name, nil))

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("wrapper %s: build timeout after %dms", cfg.Name, b.TimeoutMs)
		}
		return fmt.Errorf("wrapper %s: build failed: %w", cfg.Name, err)
	}
	return nil
}

// startWrapperProcess
//
// Functional role (Brique DSL):
// - start wrapper runtime process from run config and return spawned process PID.
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
// - derives wrapper source/build/work directories from the owning context dir.
// - creates build/work directories best effort before process start.
// - starts the configured wrapper runtime process.
// - streams child stdout/stderr to the current process.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - cfg WrapperRuntimeCfg.
//
//
// Outputs:
// - returns (pid int, err error).
//
//
// Contract:
// - Process supervision and readiness tracking continue outside this helper.
// - Interpreted wrappers run from `<ctxDir>/code/<wrapper_name>`.
// - Compiled wrappers run from `<ctxDir>/build/<wrapper_name>`.
// - `BRIQUE_WORKDIR` points to `<ctxDir>/tmp/wrapper_<wrapper_name>`.
// - Invalid wrapper mode or start failure returns an error without mutating wrapper runtime state by itself.

func (l *ExecutionLoop) startWrapperProcess(cfg WrapperRuntimeCfg) (pid int, err error) {
	if len(cfg.Run.Cmd) == 0 {
		return 0, fmt.Errorf("wrapper %s: empty run cmd", cfg.Name)
	}
	sourceDir, ok := l.wrapperSourceDir(cfg.Name)
	if !ok {
		return 0, fmt.Errorf("wrapper %s: missing context dir", cfg.Name)
	}
	buildDir, _ := l.wrapperBuildDir(cfg.Name)
	workDir, _ := l.wrapperWorkDir(cfg.Name)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return 0, fmt.Errorf("wrapper %s: prepare build dir: %w", cfg.Name, err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return 0, fmt.Errorf("wrapper %s: prepare work dir: %w", cfg.Name, err)
	}

	//we treat compiled/interpreted the same here:
	// - build step decides what exists
	// - run step decides how to execute it (binary or interpreter)
	// (Mode can still be validated upstream if you want.)
	switch cfg.Mode {
	case configuration.ValueConfigStyleCompiled, configuration.ValueConfigStyleInterpreted:
	default:
		return 0, fmt.Errorf("wrapper %s: invalid mode %q", cfg.Name, cfg.Mode)
	}

	run := cfg.Run
	if cfg.Mode == configuration.ValueConfigStyleCompiled {
		run.Cmd = applyCompiledWrapperExeExt(run.Cmd, true)
	}
	if venvBin, ok := l.instancePythonVenvBin(cfg.Run.EnvName); ok {
		run.Cmd = rewriteCmdForVenv(run.Cmd, venvBin)
	}
	cmd := buildRunCommand(run)

	// Runtime cwd is derived from context ownership, never from config.
	switch cfg.Mode {
	case configuration.ValueConfigStyleCompiled:
		cmd.Dir = buildDir
	case configuration.ValueConfigStyleInterpreted:
		cmd.Dir = sourceDir
	}

	// environment: inherit current process env + engine/runtime overrides
	cmd.Env = mergeEnv(os.Environ(), l.wrapperRuntimeEnv(cfg.Name, cfg.Run.Env))

	// Wrapper runtimes are long-lived child processes. Do not inherit the test
	// process stdout/stderr directly, otherwise lingering wrapper shutdown can
	// keep go test I/O open after PASS and trip WaitDelay.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("wrapper %s: start failed: %w", cfg.Name, err)
	}
	if cmd.Process == nil {
		return 0, fmt.Errorf("wrapper %s: started but no process handle", cfg.Name)
	}
	pid = cmd.Process.Pid
	// Execution supervises wrappers by pid only. Releasing the started process
	// handle avoids leaking unreaped os/exec bookkeeping across long-lived test
	// suites while preserving later best-effort stop/kill by pid.
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("wrapper %s: release process handle: %w", cfg.Name, err)
	}
	return pid, nil
}

// platformExeExt is the executable file suffix on the current OS: ".exe" on
// Windows, "" elsewhere.
func platformExeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ensurePlatformExeExt appends the platform's executable suffix to path when
// missing (a no-op on macOS/Linux, where the suffix is empty). An architect
// writes a compiled wrapper's build output path and run command as if always
// on macOS/Linux — never typing ".exe" — and the engine appends it here on
// Windows so the same config produces and launches the right file on every OS.
func ensurePlatformExeExt(path string) string {
	ext := platformExeExt()
	if ext == "" || strings.HasSuffix(path, ext) {
		return path
	}
	return path + ext
}

// applyCompiledWrapperExeExt returns a copy of a compiled wrapper's Build.Cmd
// or Run.Cmd with the platform's executable suffix applied to its binary
// output path — the element right after a "-o" flag for Build.Cmd, or the
// first element for Run.Cmd (isRunCmd true). Every other element is left
// untouched. No-op when cmd is empty, or (for Build.Cmd) when no "-o" flag is
// present — a build command with no explicit output path names nothing for
// the engine to correct, and is left to whatever default the build tool uses.
func applyCompiledWrapperExeExt(cmd []string, isRunCmd bool) []string {
	if len(cmd) == 0 {
		return cmd
	}
	out := make([]string, len(cmd))
	copy(out, cmd)
	if isRunCmd {
		out[0] = ensurePlatformExeExt(out[0])
		return out
	}
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "-o" {
			out[i+1] = ensurePlatformExeExt(out[i+1])
			break
		}
	}
	return out
}

// buildRunCommand
//
// Functional role (Brique DSL):
// - build executable command from run config in shell or argv mode.
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
// - allocates one `exec.Cmd` value.
//
// Inputs:
// - run RunCfg.
//
//
// Outputs:
// - returns `*exec.Cmd`.
//
//
// Contract:
// - Shell mode joins `Run.Cmd` into one command line before wrapping it in the platform shell.
// - Non-shell mode assumes `Run.Cmd` is non-empty and indexes `run.Cmd[0]` directly.

func buildRunCommand(run RunCfg) *exec.Cmd {
	if run.Shell {
		// Run.Cmd is interpreted as a shell command line, run through the
		// platform shell below (sh -lc on macOS/Linux, cmd.exe /C on Windows).
		// Example (macOS/Linux): []string{"source venv/bin/activate && python main.py"}
		// Example (Windows):     []string{"venv\\Scripts\\activate.bat && python main.py"}
		s := strings.Join(run.Cmd, " ")

		if runtime.GOOS == "windows" {
			return exec.Command("cmd.exe", "/C", s)
		}
		return exec.Command("sh", "-lc", s)
	}

	// Non-shell mode: classic exec with args
	return exec.Command(run.Cmd[0], run.Cmd[1:]...)
}

func (l *ExecutionLoop) wrapperSourceDir(wrapper string) (string, bool) {
	if l == nil || l.frame == nil || strings.TrimSpace(l.frame.ContextDir) == "" || strings.TrimSpace(wrapper) == "" {
		return "", false
	}
	return filepath.Join(l.frame.ContextDir, shared.ContextDirCode, wrapper), true
}

func (l *ExecutionLoop) wrapperBuildDir(wrapper string) (string, bool) {
	if l == nil || l.frame == nil || strings.TrimSpace(l.frame.ContextDir) == "" || strings.TrimSpace(wrapper) == "" {
		return "", false
	}
	return filepath.Join(l.frame.ContextDir, shared.ContextDirBuild, wrapper), true
}

func (l *ExecutionLoop) wrapperWorkDir(wrapper string) (string, bool) {
	if l == nil || l.frame == nil || strings.TrimSpace(l.frame.ContextDir) == "" || strings.TrimSpace(wrapper) == "" {
		return "", false
	}
	return filepath.Join(l.frame.ContextDir, shared.ContextDirTmp, "wrapper_"+wrapper), true
}

// venvBinDirName is the subdirectory holding a Python venv's executables:
// "Scripts" on Windows, "bin" everywhere else.
func venvBinDirName() string {
	if runtime.GOOS == "windows" {
		return "Scripts"
	}
	return "bin"
}

// instancePythonVenvBin resolves the bin directory of a shared Python venv
// living next to the instance root (sibling of the context whose
// brique.ctx_type == "root"), by walking up from the wrapper's own context dir.
//
// Each venv is created and maintained by hand (pip install -r requirements.txt);
// this helper only locates an already-existing bin dir, never creates one.
//
// - envName == ""  => <instance_parent>/venv/<bin-dir> (single shared default env).
// - envName != ""  => <instance_parent>/envs/<envName>/venv/<bin-dir> (named env, for
//   wrappers whose dependencies are incompatible with the default env).
// - <bin-dir> is "Scripts" on Windows, "bin" on macOS/Linux.
func (l *ExecutionLoop) instancePythonVenvBin(envName string) (string, bool) {
	if l == nil || l.frame == nil || strings.TrimSpace(l.frame.ContextDir) == "" {
		return "", false
	}
	instanceParent, ok := instanceRootParentDir(l.frame.ContextDir)
	if !ok {
		return "", false
	}
	envName = strings.TrimSpace(envName)
	binDir := venvBinDirName()
	var venvBin string
	if envName == "" {
		venvBin = filepath.Join(instanceParent, "venv", binDir)
	} else {
		venvBin = filepath.Join(instanceParent, "envs", envName, "venv", binDir)
	}
	if info, err := os.Stat(venvBin); err != nil || !info.IsDir() {
		return "", false
	}
	return venvBin, true
}

// instanceRootParentDir walks up from ctxDir through parent context.json
// descriptors until it finds the one declaring brique.ctx_type == "root",
// and returns that root context directory's parent (where requirements.txt
// and venv/ live, as siblings of the instance root).
func instanceRootParentDir(ctxDir string) (string, bool) {
	dir := ctxDir
	for {
		if isRootContextDir(dir) {
			parent := filepath.Dir(dir)
			if parent == "" || parent == dir {
				return "", false
			}
			return parent, true
		}
		parent := filepath.Dir(dir)
		if parent == "" || parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isRootContextDir(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		return false
	}
	var doc struct {
		Brique struct {
			CtxType string `json:"ctx_type"`
		} `json:"brique"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return false
	}
	return doc.Brique.CtxType == "root"
}

// venvPythonExecutableName is the interpreter filename inside a venv's bin
// directory: "python.exe" on Windows (python -m venv never creates a
// "python3.exe" there), "python3" everywhere else.
func venvPythonExecutableName() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

// rewriteCmdForVenv replaces a bare "python3"/"python" interpreter at the
// front of cmd with the shared instance venv's interpreter, leaving any other
// command (compiled binaries, node, etc.) untouched.
func rewriteCmdForVenv(cmd []string, venvBin string) []string {
	if len(cmd) == 0 {
		return cmd
	}
	switch cmd[0] {
	case "python3", "python":
		out := make([]string, len(cmd))
		copy(out, cmd)
		out[0] = filepath.Join(venvBin, venvPythonExecutableName())
		return out
	default:
		return cmd
	}
}

func (l *ExecutionLoop) wrapperRuntimeEnv(wrapper string, override map[string]string) map[string]string {
	out := map[string]string{}
	if l != nil && l.frame != nil && strings.TrimSpace(l.frame.ContextDir) != "" {
		out["BRIQUE_CTX_DIR"] = l.frame.ContextDir
		out["BRIQUE_CTX_ID"] = string(l.frame.CtxId)
	}
	if addr := l.webSocketListenerAddr(); addr != "" {
		out["BRIQUE_WS_ADDR"] = addr
		if baseURL := webSocketBaseURL(addr); baseURL != "" {
			out["BRIQUE_WS_URL"] = baseURL
		}
	}
	if strings.TrimSpace(wrapper) != "" {
		out["BRIQUE_WRAPPER_NAME"] = wrapper
		if v, ok := l.wrapperSourceDir(wrapper); ok {
			out["BRIQUE_WRAPPER_SRC_DIR"] = v
		}
		if v, ok := l.wrapperBuildDir(wrapper); ok {
			out["BRIQUE_WRAPPER_BUILD_DIR"] = v
		}
		if v, ok := l.wrapperWorkDir(wrapper); ok {
			out["BRIQUE_WORKDIR"] = v
		}
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

type webSocketListenerAddrProvider interface {
	WebSocketListenerAddr() string
}

func (l *ExecutionLoop) webSocketListenerAddr() string {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return ""
	}
	provider, ok := l.frame.CtxCommReg.(webSocketListenerAddrProvider)
	if !ok {
		return ""
	}
	return strings.TrimSpace(provider.WebSocketListenerAddr())
}

func webSocketBaseURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host := "127.0.0.1"
	if strings.HasPrefix(addr, ":") {
		port := strings.TrimPrefix(addr, ":")
		if port == "" {
			return ""
		}
		return fmt.Sprintf("ws://%s:%s", host, port)
	}
	h, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	if h != "" && h != "0.0.0.0" && h != "::" {
		host = h
	}
	return fmt.Sprintf("ws://%s:%s", host, port)
}

// mergeEnv
//
// Functional role (Brique DSL):
// - merge environment overrides onto inherited environment list.
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
// - allocates a merged environment slice when overrides are provided.
//
// Inputs:
// - base []string, override map[string]string.
//
//
// Outputs:
// - returns []string.
//
//
// Contract:
// - First occurrence order from `base` is preserved; override keys replace existing values or append new keys.
// - When `override` is empty, returns the original `base` slice unchanged.

func mergeEnv(base []string, override map[string]string) []string {
	if len(override) == 0 {
		return base
	}

	// Parse base into a map to avoid duplicates and ensure deterministic override.
	env := make(map[string]string, len(base)+len(override))
	order := make([]string, 0, len(base)+len(override))

	for _, kv := range base {
		if kv == "" {
			continue
		}
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		v := kv[i+1:]
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}

	for k, v := range override {
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

// waitWrapperReady
//
// Functional role (Brique DSL):
// - wait for wrapper readiness signal with optional timeout and loop-stop cancellation.
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
// - waits on the ready channel, timeout timer, and execution loop shutdown channel.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
// - ch chan struct{}, timeoutMs int.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Non-positive timeout waits indefinitely until ready or loop stop.
// - Returns `"stopped"` when loop shutdown wins, and `"timeout"` when the timer wins.

func (l *ExecutionLoop) waitWrapperReady(ch chan struct{}, timeoutMs int) error {
	if timeoutMs <= 0 {
		select {
		case <-l.done:
			return fmt.Errorf("stopped")
		case <-ch:
			return nil
		}
	}

	t := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer t.Stop()

	select {
	case <-l.done:
		return fmt.Errorf("stopped")
	case <-ch:
		return nil
	case <-t.C:
		return fmt.Errorf("timeout")
	}
}

// getWrapperCfg
//
// Functional role (Brique DSL):
// - look up wrapper runtime config by name from parsed execution configuration.
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
// - receiver `l *ExecutionLoop`.
// - name string.
//
//
// Outputs:
// - returns (WrapperRuntimeCfg, bool).
//
//
// Contract:
// - Linear scan over configured wrappers.
// - Returns `(WrapperRuntimeCfg{}, false)` when no wrapper with the exact configured name exists.

func (l *ExecutionLoop) getWrapperCfg(name string) (WrapperRuntimeCfg, bool) {
	for i := range l.cfg.Wrappers {
		w := l.cfg.Wrappers[i]
		if w.Name == name {
			return w, true
		}
	}
	return WrapperRuntimeCfg{}, false
}

// stopAllWrappersBestEffort
//
// Functional role (Brique DSL):
// - >sequence:
//   - iterate configured wrappers
//   - mark local wrapper state as stopping
//   - emit wrapper-stop intention to wrapper transport root
//   - when stop timeout is configured and PID is known, spawn watchdog goroutine
//   - watchdog force-kills still-live wrapper after timeout and updates wrapper state best effort
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
//   - none directly from this function.
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
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind and intention when forwarding wrapper-stop requests to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates wrapper runtime state to stopping.
// - may spawn watchdog goroutines that force-kill wrapper processes after timeout.
//
// Inputs:
// - receiver `l *ExecutionLoop`.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
//
// Contract:
// - Shutdown helper with best-effort process enforcement; absence of wrapper state or PID silently skips force-kill path.
// - Emits one wrapper-stop intention per configured wrapper with a non-empty name when wrapper state exists.
// - Watchdog force-kill is best effort and only runs when `StopTimeoutMs > 0` and a PID is known.

func (l *ExecutionLoop) stopAllWrappersBestEffort() {
	for i := range l.cfg.Wrappers {
		w := l.cfg.Wrappers[i]
		if w.Name == "" {
			continue
		}
		// Mark local state
		st, ok := l.frame.Wrappers[w.Name]
		if !ok {
			continue
		}
		st.Lock()
		st.ProcState = junction.ProcStopping
		st.StopRequestedAt = time.Now()
		pid := st.PID
		st.Unlock()

		// Emit stop intention via Comm
		l.emitToComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: circulation.NewIntentionID(),
				From: circulation.Address{
					Context: circulation.ContextID(l.frame.CtxId),
					Type:    circulation.ValueTypeExecution,
					Cap:     execCapWrapperStop,
				},
				To: circulation.Address{
					Context: circulation.ContextID("@wrapper_" + w.Name + ":/"),
					Type:    circulation.ValueTypeExecution,
					Cap:     execCapWrapperStop,
				},
				Params: map[string]any{},
			},
		})

		// If wrapper does not stop within StopTimeoutMs, force-kill the process.
		if w.StopTimeoutMs > 0 && pid > 0 {
			timeout := time.Duration(w.StopTimeoutMs) * time.Millisecond
			l.wg.Add(1)
			go func(wrapperName string, pid int, timeout time.Duration) {
				defer l.wg.Done()
				t := time.NewTimer(timeout)
				defer t.Stop()
				select {
				case <-l.done:
					return
				case <-t.C:
				}

				st, ok := l.frame.Wrappers[wrapperName]
				if !ok {
					return
				}
				st.Lock()
				// Still stopping? then force kill.
				shouldKill := (st.ProcState == junction.ProcStopping || st.ProcState == junction.ProcRunning || st.ProcState == junction.ProcStarting)
				st.Unlock()
				if !shouldKill {
					return
				}

				p, err := os.FindProcess(pid)
				if err == nil && p != nil {
					_ = p.Kill()
				}

				// Best-effort state transition: we don't have a wrapper_exited signal yet.
				st.Lock()
				// If still in a "live" state, mark as exited after forced kill.
				if st.ProcState == junction.ProcStopping || st.ProcState == junction.ProcRunning || st.ProcState == junction.ProcStarting {
					st.ProcState = junction.ProcExited
					st.LastExitAt = time.Now()
					st.LastError = fmt.Errorf("wrapper %s: killed after stop timeout", wrapperName)
					st.PID = 0
					st.Ready = false
					st.SetStarting(false)
					st.SetBuilding(false)
					st.ReadyAt = time.Time{}
					if st.ReadyErr == nil {
						st.ReadyErr = st.LastError
					}
					ch := st.GetReadyCh()
					if ch != nil {
						close(ch)
						st.ResetReadyCh()
					}
				}
				st.Unlock()
			}(w.Name, pid, timeout)
		}
	}
}
