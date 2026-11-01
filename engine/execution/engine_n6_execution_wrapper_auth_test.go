package execution_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
)

// setRootWrapperAuth rewrites the root listener config with a wrapper_auth mode.
func setRootWrapperAuth(t *testing.T, rootDir, mode string) {
	t.Helper()
	path := filepath.Join(rootDir, "context.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	brique, _ := doc[circulation.KeyBrique].(map[string]any)
	engineConfig, _ := brique["engine_config"].(map[string]any)
	commCfg, _ := engineConfig["communication"].(map[string]any)
	listener, _ := commCfg["websocket_listener"].(map[string]any)
	if listener == nil {
		t.Fatal("root has no websocket_listener")
	}
	listener["wrapper_auth"] = mode
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// With wrapper_auth "required", the real Python wrapper library, unchanged,
// still connects: the secret travels inside BRIQUE_WS_URL.
func TestEngine_N6_EXEC_WrapperAuthRequiredRealPythonWrapper(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	if _, err := exec.LookPath("c++"); err != nil {
		t.Skipf("c++ unavailable: %v", err)
	}
	rootDir := copyN6ExecutionSandboxRoot(t)
	patchN6ExecutionSandboxForRuntime(t, rootDir)
	setRootWrapperAuth(t, rootDir, "required")
	h := startEngineN6ExecutionHarnessOnRootDir(t, rootDir)

	resp := callN6ExecutionUserCap(t, h, n6ExecutionWorkspaceID, "n6-exec-auth-echo", "sandbox.py.echo", map[string]any{
		"message": "hello-secret",
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("python echo status=%q payload=%#v error=%#v", resp.Status, resp.Payload, resp.Error)
	}
	if payload := mustExecutionPayloadMap(t, resp.Payload); payload["echo"] != "hello-secret" {
		t.Fatalf("payload=%#v", payload)
	}
}
