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
	"strings"
	"testing"
	"time"

	"brique_engine/configuration"
)

const testSecret = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde"

func logTestCfg(script string, logBlock map[string]any) map[string]any {
	cfg := procTestCfg([]any{"sh", "-c", script}, false, 0, 0)
	run := cfg[configuration.KeyWrappers].([]any)[0].(map[string]any)[configuration.KeyWrpRun].(map[string]any)
	if logBlock != nil {
		run[configuration.KeyWrpLog] = logBlock
	}
	return cfg
}

// spawnAndWaitExit starts the wrapper and waits until its exit has been applied to the state.
func spawnAndWaitExit(t *testing.T, l *ExecutionLoop) error {
	t.Helper()
	st := l.frame.Wrappers["w1"]
	spawnTracked(t, l, false)
	st.Lock()
	ch := st.GetReadyCh()
	st.Unlock()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("the wrapper exit was not applied")
	}
	st.Lock()
	defer st.Unlock()
	return st.ReadyErr
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(dir + "/tmp/wrapper_w1/wrapper.log")
	if err != nil {
		t.Fatalf("wrapper log not written: %v", err)
	}
	return string(data)
}

func TestWrapperLog_ConfigDefaultsOff(t *testing.T) {
	l, _, _ := newProcTestLoop(t, logTestCfg("true", nil))
	cfg, _ := l.getWrapperCfg("w1")
	if cfg.Run.Log.Enabled {
		t.Fatalf("the wrapper log must be off by default")
	}
	l2, _, _ := newProcTestLoop(t, logTestCfg("true", map[string]any{configuration.KeyWrpLogEnabled: true, configuration.KeyWrpLogMaxBytes: 4096}))
	cfg2, _ := l2.getWrapperCfg("w1")
	if !cfg2.Run.Log.Enabled || cfg2.Run.Log.MaxBytes != 4096 {
		t.Fatalf("run.log not parsed: %+v", cfg2.Run.Log)
	}
}

func TestWrapperLog_DisabledWritesNothingAndKeepsErrorUnchanged(t *testing.T) {
	l, _, dir := newProcTestLoop(t, logTestCfg("echo hello; echo oops >&2; exit 3", nil))
	err := spawnAndWaitExit(t, l)
	if err == nil || err.Error() != "wrapper w1: process exited before ready" {
		t.Fatalf("the readiness error must be unchanged without a log, got %v", err)
	}
	if _, statErr := os.Stat(dir + "/tmp/wrapper_w1/wrapper.log"); statErr == nil {
		t.Fatalf("no log file may exist when the option is off")
	}
}

func TestWrapperLog_EnabledCapturesOutputAndExplainsEarlyExit(t *testing.T) {
	l, _, dir := newProcTestLoop(t, logTestCfg("echo out-line; echo err-line >&2; exit 3", map[string]any{configuration.KeyWrpLogEnabled: true}))
	err := spawnAndWaitExit(t, l)
	if err == nil || !strings.HasPrefix(err.Error(), "wrapper w1: process exited before ready") {
		t.Fatalf("unexpected readiness error: %v", err)
	}
	for _, want := range []string{"exit status 3", "out-line", "err-line"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the readiness error must carry %q, got %v", want, err)
		}
	}
	log := readLog(t, dir)
	for _, want := range []string{"--- ", " launch cmd=sh -c ", "out-line", "err-line", " exit pid="} {
		if !strings.Contains(log, want) {
			t.Fatalf("log misses %q:\n%s", want, log)
		}
	}
}

func TestWrapperLog_NeverWritesTheLaunchSecret(t *testing.T) {
	l, _, dir := newProcTestLoop(t, logTestCfg("echo \"dial ws://127.0.0.1:8080/k/"+testSecret+"/wrapper/w1: bad status\" >&2; exit 1", map[string]any{configuration.KeyWrpLogEnabled: true}))
	err := spawnAndWaitExit(t, l)
	log := readLog(t, dir)
	if strings.Contains(log, testSecret) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("the launch secret leaked\nlog: %s\nerr: %v", log, err)
	}
	if !strings.Contains(log, "/k/<redacted>/wrapper/w1") || !strings.Contains(err.Error(), "/k/<redacted>") {
		t.Fatalf("the redacted route must remain readable\nlog: %s\nerr: %v", log, err)
	}
}

func TestWrapperLog_IsBounded(t *testing.T) {
	script := "i=0; while [ $i -lt 400 ]; do echo line-number-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; i=$((i+1)); done; exit 1"
	l, _, dir := newProcTestLoop(t, logTestCfg(script, map[string]any{configuration.KeyWrpLogEnabled: true, configuration.KeyWrpLogMaxBytes: 2048}))
	_ = spawnAndWaitExit(t, l)
	live, err := os.Stat(dir + "/tmp/wrapper_w1/wrapper.log")
	if err != nil {
		t.Fatalf("live log: %v", err)
	}
	if live.Size() > 2048 {
		t.Fatalf("the live log exceeds max_bytes: %d", live.Size())
	}
	if _, err := os.Stat(dir + "/tmp/wrapper_w1/wrapper.log.1"); err != nil {
		t.Fatalf("a rotated file must be kept: %v", err)
	}
	if !strings.Contains(readLog(t, dir), "line-number-399-") {
		t.Fatalf("the newest output must be in the live file")
	}
}

func TestWrapperLog_SecretSplitAcrossWritesIsRedacted(t *testing.T) {
	dir := t.TempDir()
	wl, err := openWrapperLog(dir, WrapperLogCfg{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = wl.Write([]byte("url ws://h/k/" + testSecret[:20]))
	_, _ = wl.Write([]byte(testSecret[20:] + "/route\n"))
	wl.Close()
	data, _ := os.ReadFile(dir + "/wrapper.log")
	if strings.Contains(string(data), testSecret[:20]) || !strings.Contains(string(data), "/k/<redacted>/route") {
		t.Fatalf("a secret split across two writes leaked: %s", data)
	}
}

func TestWrapperLog_TailOnlyCoversTheLastLaunch(t *testing.T) {
	dir := t.TempDir()
	wl, _ := openWrapperLog(dir, WrapperLogCfg{Enabled: true})
	wl.Marker("launch cmd=a")
	_, _ = wl.Write([]byte("old failure\n"))
	wl.Marker("exit pid=1 status=exit status 1")
	wl.Marker("launch cmd=a")
	_, _ = wl.Write([]byte("new failure\n"))
	tail := wl.Tail()
	wl.Close()
	if tail != "new failure" {
		t.Fatalf("tail must only cover the last launch, got %q", tail)
	}
}
