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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

func TestExecutionWrapper_N1_EXWRP_01_GetWrapperCfg(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted},
		},
	})
	cfg, ok := l.getWrapperCfg("w1")
	if !ok || cfg.Name != "w1" {
		t.Fatalf("expected wrapper cfg hit, got cfg=%#v ok=%v", cfg, ok)
	}
	if _, ok := l.getWrapperCfg("missing"); ok {
		t.Fatalf("missing wrapper should not be found")
	}
}

func TestExecutionWrapper_N1_EXWRP_02_MergeEnv(t *testing.T) {
	base := []string{"A=1", "B=2"}
	over := map[string]string{"B": "3", "C": "4"}
	out := mergeEnv(base, over)
	joined := strings.Join(out, "|")
	if !strings.Contains(joined, "A=1") || !strings.Contains(joined, "B=3") || !strings.Contains(joined, "C=4") {
		t.Fatalf("unexpected merged env: %#v", out)
	}
}

func TestExecutionWrapper_N1_EXWRP_03_BuildRunCommand(t *testing.T) {
	cmd := buildRunCommand(RunCfg{Cmd: []string{"python", "app.py"}, Shell: false})
	if cmd.Path == "" || len(cmd.Args) != 2 || cmd.Args[0] != "python" {
		t.Fatalf("unexpected non-shell command: path=%q args=%#v", cmd.Path, cmd.Args)
	}

	shellCmd := buildRunCommand(RunCfg{Cmd: []string{"echo ok"}, Shell: true})
	if runtime.GOOS == "windows" {
		if !strings.Contains(strings.ToLower(shellCmd.Path), "cmd") {
			t.Fatalf("expected cmd.exe shell path on windows, got %q", shellCmd.Path)
		}
	} else {
		if !strings.Contains(shellCmd.Path, "sh") {
			t.Fatalf("expected sh shell path, got %q", shellCmd.Path)
		}
	}
}

func TestExecutionWrapper_N1_EXWRP_03A_CompiledWrapperExeExt(t *testing.T) {
	ext := platformExeExt()
	if runtime.GOOS == "windows" {
		if ext != ".exe" {
			t.Fatalf("expected .exe on windows, got %q", ext)
		}
	} else if ext != "" {
		t.Fatalf("expected empty extension on macOS/Linux, got %q", ext)
	}

	// ensurePlatformExeExt: appends once, never doubles up.
	base := "code/wrapper/bin/wrapper"
	got := ensurePlatformExeExt(base)
	if got != base+ext {
		t.Fatalf("ensurePlatformExeExt mismatch: got %q, want %q", got, base+ext)
	}
	if again := ensurePlatformExeExt(got); again != got {
		t.Fatalf("ensurePlatformExeExt should be idempotent: got %q, want %q", again, got)
	}

	// applyCompiledWrapperExeExt, Build.Cmd side: corrects the path after "-o".
	buildIn := []string{"go", "build", "-o", "code/wrapper/bin/wrapper", "."}
	buildOut := applyCompiledWrapperExeExt(buildIn, false)
	if buildOut[3] != base+ext {
		t.Fatalf("build -o path mismatch: got %q, want %q", buildOut[3], base+ext)
	}
	if buildOut[0] != "go" || buildOut[1] != "build" || buildOut[2] != "-o" || buildOut[4] != "." {
		t.Fatalf("build cmd elements other than the -o path should be unchanged: %#v", buildOut)
	}
	if buildIn[3] != "code/wrapper/bin/wrapper" {
		t.Fatalf("applyCompiledWrapperExeExt should not mutate its input: %#v", buildIn)
	}

	// No "-o" flag: build cmd is returned unchanged (nothing for the engine to correct).
	noFlag := []string{"make", "wrapper"}
	if out := applyCompiledWrapperExeExt(noFlag, false); out[0] != "make" || out[1] != "wrapper" {
		t.Fatalf("build cmd without -o should be left untouched: %#v", out)
	}

	// applyCompiledWrapperExeExt, Run.Cmd side: corrects the first element only.
	runIn := []string{"code/wrapper/bin/wrapper", "--flag", "value"}
	runOut := applyCompiledWrapperExeExt(runIn, true)
	if runOut[0] != base+ext {
		t.Fatalf("run cmd[0] mismatch: got %q, want %q", runOut[0], base+ext)
	}
	if runOut[1] != "--flag" || runOut[2] != "value" {
		t.Fatalf("run cmd args other than cmd[0] should be unchanged: %#v", runOut)
	}

	// Empty cmd is returned as-is.
	if out := applyCompiledWrapperExeExt(nil, true); out != nil {
		t.Fatalf("nil cmd should return nil, got %#v", out)
	}
}

func TestExecutionWrapper_N1_EXWRP_03B_VenvPlatformNaming(t *testing.T) {
	binDir := venvBinDirName()
	pyExe := venvPythonExecutableName()
	if runtime.GOOS == "windows" {
		if binDir != "Scripts" {
			t.Fatalf("expected Scripts venv bin dir on windows, got %q", binDir)
		}
		if pyExe != "python.exe" {
			t.Fatalf("expected python.exe venv interpreter on windows, got %q", pyExe)
		}
	} else {
		if binDir != "bin" {
			t.Fatalf("expected bin venv dir on macOS/Linux, got %q", binDir)
		}
		if pyExe != "python3" {
			t.Fatalf("expected python3 venv interpreter on macOS/Linux, got %q", pyExe)
		}
	}

	venvBin := filepath.Join("some", "venv", binDir)
	rewritten := rewriteCmdForVenv([]string{"python3", "main.py"}, venvBin)
	wantInterpreter := filepath.Join(venvBin, pyExe)
	if len(rewritten) != 2 || rewritten[0] != wantInterpreter || rewritten[1] != "main.py" {
		t.Fatalf("rewriteCmdForVenv mismatch: got %#v, want interpreter %q", rewritten, wantInterpreter)
	}

	// "python" (no trailing 3) is rewritten the same way.
	rewrittenBare := rewriteCmdForVenv([]string{"python", "main.py"}, venvBin)
	if len(rewrittenBare) != 2 || rewrittenBare[0] != wantInterpreter {
		t.Fatalf("rewriteCmdForVenv should rewrite bare 'python' too: got %#v", rewrittenBare)
	}

	// Non-python commands are left untouched.
	untouched := rewriteCmdForVenv([]string{"node", "server.js"}, venvBin)
	if len(untouched) != 2 || untouched[0] != "node" {
		t.Fatalf("rewriteCmdForVenv should leave non-python commands untouched: got %#v", untouched)
	}

	// Empty command is returned as-is.
	if empty := rewriteCmdForVenv(nil, venvBin); len(empty) != 0 {
		t.Fatalf("rewriteCmdForVenv on empty cmd should return empty, got %#v", empty)
	}
}

func TestExecutionWrapper_N1_EXWRP_04_WaitWrapperReadyBranches(t *testing.T) {
	l1, _, _ := newExecutionLoopHarness(nil)
	ch1 := make(chan struct{})
	go func() { close(ch1) }()
	if err := l1.waitWrapperReady(ch1, 50); err != nil {
		t.Fatalf("expected ready wait success, got %v", err)
	}

	l2, _, _ := newExecutionLoopHarness(nil)
	ch2 := make(chan struct{})
	if err := l2.waitWrapperReady(ch2, 20); err == nil || err.Error() != "timeout" {
		t.Fatalf("expected timeout error, got %v", err)
	}

	l3, _, _ := newExecutionLoopHarness(nil)
	l3.once.Do(func() { close(l3.done) })
	ch3 := make(chan struct{})
	if err := l3.waitWrapperReady(ch3, 100); err == nil || err.Error() != "stopped" {
		t.Fatalf("expected stopped error, got %v", err)
	}
}

func TestExecutionWrapper_N1_EXWRP_05_EnsureWrapperReadyMissingCfg(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	in := circulation.Intention{IntentionID: "i1", From: circulation.Address{Context: "/ctx/caller"}, To: circulation.Address{Cap: "cap"}, Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); !abort {
		t.Fatalf("missing wrapper cfg should abort")
	}
	out := recvExecMsg(t, commCh, "missing wrapper cfg error")
	if out.Kind != circulation.ValueKindResponse || out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("unexpected response: %#v", out)
	}
}

func TestExecutionWrapper_N1_EXWRP_06_EnsureWrapperReadyFastPath(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted, configuration.KeyWrpRun: map[string]any{configuration.KeyWrpCmd: []any{"echo", "ok"}}},
		},
	})
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = true
	st.ReadyErr = nil
	st.Unlock()

	in := circulation.Intention{IntentionID: "i2", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); abort {
		t.Fatalf("ready fast path should not abort")
	}
	select {
	case m := <-commCh:
		t.Fatalf("no response expected on fast path, got %#v", m)
	default:
	}
}

func TestExecutionWrapper_N1_EXWRP_07_EnsureWrapperReadyFailFastReadyErr(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted, configuration.KeyWrpRun: map[string]any{configuration.KeyWrpCmd: []any{"echo", "ok"}}},
		},
	})
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcUnknown
	st.ReadyErr = errTest("ready failed")
	st.Unlock()

	in := circulation.Intention{IntentionID: "i3", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); !abort {
		t.Fatalf("readyErr fail-fast should abort")
	}
	out := recvExecMsg(t, commCh, "readyErr response")
	if out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unexpected response for readyErr: %#v", out)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestExecutionWrapper_N1_EXWRP_08_EnsureWrapperReadyInvalidMode(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: "bad-mode"},
		},
	})
	in := circulation.Intention{IntentionID: "i4", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); !abort {
		t.Fatalf("invalid mode should abort")
	}
	out := recvExecMsg(t, commCh, "invalid mode response")
	if out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeConfiguration {
		t.Fatalf("unexpected invalid mode response: %#v", out)
	}
}

func TestExecutionWrapper_N1_EXWRP_09_EnsureWrapperReadyWaitBranchSuccess(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted, configuration.KeyWrpReadyTO: 200, configuration.KeyWrpRun: map[string]any{configuration.KeyWrpCmd: []any{"echo", "ok"}}},
		},
	})
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.Ready = false
	st.ReadyErr = nil
	st.SetStarting(true)
	st.SetReadyCh()
	ch := st.GetReadyCh()
	st.Unlock()

	go func() {
		time.Sleep(20 * time.Millisecond)
		st.Lock()
		st.Ready = true
		st.Unlock()
		close(ch)
	}()

	in := circulation.Intention{IntentionID: "i5", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); abort {
		t.Fatalf("wait branch success should not abort")
	}
}

func TestExecutionWrapper_N1_EXWRP_10_EnsureWrapperReadyStartFailure(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted, configuration.KeyWrpRun: map[string]any{configuration.KeyWrpCmd: []any{}}},
		},
	})
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcUnknown
	st.ReadyErr = nil
	st.Unlock()

	in := circulation.Intention{IntentionID: "i6", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); !abort {
		t.Fatalf("start failure should abort")
	}
	out := recvExecMsg(t, commCh, "start failure response")
	if out.Response.Error == nil || out.Response.Error.Code != circulation.ValueCodeRefused {
		t.Fatalf("unexpected start-failure response: %#v", out)
	}
	st.Lock()
	defer st.Unlock()
	if st.ReadyErr == nil {
		t.Fatalf("start failure should set ReadyErr")
	}
}

func TestExecutionWrapper_N1_EXWRP_11_StopAllWrappersBestEffortEmitsStop(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted, configuration.KeyWrpStopTO: 0},
		},
	})
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ProcState = junction.ProcRunning
	st.PID = 0
	st.Unlock()

	l.stopAllWrappersBestEffort()
	out := recvExecMsg(t, commCh, "wrapper stop intention")
	if out.Kind != circulation.ValueKindIntention || out.Intention.To.Cap != execCapWrapperStop {
		t.Fatalf("unexpected stop message: %#v", out)
	}
	if string(out.Intention.To.Context) != "@wrapper_w1:/" {
		t.Fatalf("unexpected stop destination: %q", out.Intention.To.Context)
	}
	st.Lock()
	defer st.Unlock()
	if st.ProcState != junction.ProcStopping {
		t.Fatalf("expected ProcStopping, got %v", st.ProcState)
	}
}

func TestExecutionWrapper_N1_EXWRP_12_BuildStartValidationGuards(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	if err := l.buildWrapper(WrapperRuntimeCfg{Name: "w0", Build: nil}); err == nil {
		t.Fatalf("buildWrapper should fail on missing build cfg")
	}
	if err := l.buildWrapper(WrapperRuntimeCfg{Name: "w0", Build: &BuildCfg{Cmd: []string{}}}); err == nil {
		t.Fatalf("buildWrapper should fail on empty cmd")
	}
	l.frame.ContextDir = ""
	if err := l.buildWrapper(WrapperRuntimeCfg{Name: "w0", Build: &BuildCfg{Cmd: []string{"echo", "ok"}}}); err == nil {
		t.Fatalf("buildWrapper should fail on missing context dir")
	}
	l.frame.ContextDir = "/tmp/brique_ctx"

	if _, err := l.startWrapperProcess(WrapperRuntimeCfg{Name: "w0", Mode: configuration.ValueConfigStyleInterpreted, Run: RunCfg{Cmd: nil}}); err == nil {
		t.Fatalf("startWrapperProcess should fail on empty run cmd")
	}
	if _, err := l.startWrapperProcess(WrapperRuntimeCfg{Name: "w0", Mode: "bad", Run: RunCfg{Cmd: []string{"echo", "ok"}}}); err == nil {
		t.Fatalf("startWrapperProcess should fail on invalid mode")
	}
	l.frame.ContextDir = ""
	if _, err := l.startWrapperProcess(WrapperRuntimeCfg{Name: "w0", Mode: configuration.ValueConfigStyleInterpreted, Run: RunCfg{Cmd: []string{"echo", "ok"}}}); err == nil {
		t.Fatalf("startWrapperProcess should fail on missing context dir")
	}
}

func TestExecutionWrapper_N1_EXWRP_13_DeriveWrapperRuntimeDirsAndEnv(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	l.frame.ContextDir = "/tmp/brique_ctx"
	l.frame.CtxCommReg.(*mockExecCommReg).webSocketAddr = ":8080"

	src, ok := l.wrapperSourceDir("w1")
	if !ok || src != filepath.Join("/tmp/brique_ctx", "code", "w1") {
		t.Fatalf("unexpected source dir: %q ok=%v", src, ok)
	}
	build, ok := l.wrapperBuildDir("w1")
	if !ok || build != filepath.Join("/tmp/brique_ctx", "build", "w1") {
		t.Fatalf("unexpected build dir: %q ok=%v", build, ok)
	}
	work, ok := l.wrapperWorkDir("w1")
	if !ok || work != filepath.Join("/tmp/brique_ctx", "tmp", "wrapper_w1") {
		t.Fatalf("unexpected work dir: %q ok=%v", work, ok)
	}

	env := l.wrapperRuntimeEnv("w1", map[string]string{"X": "1"})
	if env["BRIQUE_CTX_DIR"] != "/tmp/brique_ctx" || env["BRIQUE_WRAPPER_NAME"] != "w1" {
		t.Fatalf("unexpected base runtime env: %#v", env)
	}
	if env["BRIQUE_WRAPPER_SRC_DIR"] != src || env["BRIQUE_WRAPPER_BUILD_DIR"] != build || env["BRIQUE_WORKDIR"] != work {
		t.Fatalf("unexpected derived runtime env: %#v", env)
	}
	if env["BRIQUE_WS_ADDR"] != ":8080" || env["BRIQUE_WS_URL"] != "ws://127.0.0.1:8080" {
		t.Fatalf("unexpected websocket runtime env: %#v", env)
	}
	if env["X"] != "1" {
		t.Fatalf("expected override env to be preserved: %#v", env)
	}
}
