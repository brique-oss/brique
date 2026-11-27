/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/engine/configuration"
)

// storeWrapperCfg is a wrapper that writes what it was told about its store and the machine directory, then waits.
func storeWrapperCfg(out string, store bool) map[string]any {
	run := map[string]any{
		configuration.KeyWrpCmd:   []any{"sh", "-c", `printf '%s|%s' "${BRIQUE_WRAPPER_STORE-unset}" "${BRIQUE_SECURITY_DIR-unset}" > "$OUT"; sleep 5`},
		configuration.KeyWrpShell: false,
		configuration.KeyWrpEnv:   map[string]any{"OUT": out},
	}
	if store {
		run[configuration.KeyWrpStore] = true
	}
	return map[string]any{configuration.KeyWrappers: []any{map[string]any{
		configuration.KeyWrpName: "w1", configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
		configuration.KeyWrpReadyTO: 0, configuration.KeyWrpStopTO: 200, configuration.KeyWrpRun: run,
	}}}
}

func storeLoop(t *testing.T, store bool, machine string) (*ExecutionLoop, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	l, _, _ := newProcTestLoop(t, storeWrapperCfg(out, store))
	l.frame.CtxCommReg = secretsCommReg{mockExecCommReg: l.frame.CtxCommReg.(*mockExecCommReg), machine: machine}
	return l, out
}

func TestWrapperStore_ADeclaredStoreIsAPrivateDirectoryAndTheMachineDirectoryIsNotPassed(t *testing.T) {
	machine := t.TempDir()
	t.Setenv("BRIQUE_SECURITY_DIR", machine) // exported by the operator: it must not reach the wrapper
	l, out := storeLoop(t, true, machine)
	spawnTracked(t, l, true)
	got := strings.Split(readWhenWritten(t, out), "|")
	want := filepath.Join(machine, "wrappers", "w1")
	if len(got) != 2 || got[0] != want {
		t.Fatalf("store = %v, want %s", got, want)
	}
	if got[1] != "unset" {
		t.Fatalf("the wrapper was told the machine directory: %s", got[1])
	}
	for _, dir := range []string{filepath.Join(machine, "wrappers"), want} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, mode %v, want 0700", dir, err, info)
		}
	}
}

func TestWrapperStore_AWrapperWithoutDeclarationGetsNoStore(t *testing.T) {
	machine := t.TempDir()
	t.Setenv("BRIQUE_SECURITY_DIR", machine)
	l, out := storeLoop(t, false, machine)
	spawnTracked(t, l, true)
	if got := readWhenWritten(t, out); got != "unset|unset" {
		t.Fatalf("an undeclared wrapper saw %q", got)
	}
	if _, err := os.Stat(filepath.Join(machine, "wrappers")); !os.IsNotExist(err) {
		t.Fatal("a directory was created for a wrapper that declares no store")
	}
}

func TestWrapperStore_NoMachineDirectoryRefusesTheLaunchWithoutRememberingIt(t *testing.T) {
	l, _ := storeLoop(t, true, "")
	cfg, _ := l.getWrapperCfg("w1")
	_, err := l.startWrapperProcess(cfg)
	if err == nil || !strings.Contains(err.Error(), "private store") {
		t.Fatalf("launch without a machine directory: %v", err)
	}
	if !isWrapperSecretError(err) {
		t.Fatal("the refusal would be remembered: adding the machine directory would not mend it")
	}
}
