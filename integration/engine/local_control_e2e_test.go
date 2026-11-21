package engine_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/engine/browserbridge"
	"brique_engine/engine/localruntime"
	"brique_engine/engine/security/secadmin"
	"brique_engine/platform/locking"
)

func TestEnginePublishesReadyAndStopsThroughLocalControl(t *testing.T) {
	buildDirectory := t.TempDir()
	binary := filepath.Join(buildDirectory, "brique-engine")
	if err := exec.Command("go", "build", "-o", binary, "brique_engine/cmd/brique").Run(); err != nil {
		t.Fatalf("build engine: %v", err)
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	contextJSON := []byte(`{
  "objective":{"name":"root","description":"local control test"},
  "functional":{"role":"root"},
  "subjective":{},
  "brique":{
    "children_list":[],"ctx_ext_name":"root","ctx_name":"root","ctx_type":"root","ctx_version":"1",
    "engine_config":{"communication":{"websocket_listener":{"addr":"127.0.0.1:1"}},"execution":{},"reflexive":{},"trace":{}},
    "engine_version":"0.0.1","type":"local"
  }
}`)
	if err := os.WriteFile(filepath.Join(root, "context.json"), contextJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-runtime-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })

	var output bytes.Buffer
	command := exec.Command(binary, "-root_path", root, "-runtime_dir", runtimeDirectory, "-websocket_addr", "127.0.0.1:0")
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-exited
		}
	})

	rootID, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	discoveryKey, _ := localruntime.DiscoveryKey(rootID)
	var descriptor localruntime.EngineRuntimeDescriptor
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("engine exited before readiness: %v\n%s", err, output.String())
		default:
		}
		descriptor, err = localruntime.LoadDescriptor(runtimeDirectory, discoveryKey)
		if err == nil && descriptor.State == localruntime.StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descriptor.State != localruntime.StateReady {
		t.Fatalf("engine never became ready: descriptor=%#v err=%v\n%s", descriptor, err, output.String())
	}
	if descriptor.UIWebSocket == "" || descriptor.UIWebSocket == "127.0.0.1:0" {
		t.Fatalf("effective UI websocket address was not published: %#v", descriptor)
	}
	if descriptor.BrowserBridge != nil {
		t.Fatalf("browser bridge was published without explicit root activation: %#v", descriptor.BrowserBridge)
	}
	response, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{
		Format: localruntime.LocalControlProtocol, Generation: descriptor.Generation, Operation: localruntime.OperationStop,
	})
	if err != nil || !response.OK {
		t.Fatalf("stop response = %#v, %v", response, err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("ordered engine stop: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-exited
		t.Fatalf("engine did not stop\n%s", output.String())
	}
	if _, err := localruntime.LoadDescriptor(runtimeDirectory, discoveryKey); err == nil {
		t.Fatal("engine descriptor remained after ordered shutdown")
	}
}

func TestEngineMountsConfiguredBrowserBridgeAndPublishesItsEndpoint(t *testing.T) {
	buildDirectory := t.TempDir()
	binary := filepath.Join(buildDirectory, "brique-engine")
	if err := exec.Command("go", "build", "-o", binary, "brique_engine/cmd/brique").Run(); err != nil {
		t.Fatalf("build engine: %v", err)
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	contextJSON := []byte(`{
  "objective":{"name":"root","description":"browser bridge mount test"},
  "functional":{"role":"root"},"subjective":{},
  "brique":{"children_list":[],"ctx_ext_name":"root","ctx_name":"root","ctx_type":"root","ctx_version":"1",
    "engine_config":{"communication":{"websocket_listener":{"addr":"127.0.0.1:1"},
      "browser_bridge":{"enabled":true,"allow_public_internet":true,"allow_cleartext_loopback":false,
        "allowed_prefixes":[],"max_addresses":8,"open_timeout_ms":15000}},
      "execution":{},"reflexive":{},"trace":{}},"engine_version":"0.0.1","type":"local"}
}`)
	if err := os.WriteFile(filepath.Join(root, "context.json"), contextJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	securityDirectory := filepath.Join(t.TempDir(), "security")
	initialized, err := (&secadmin.Dir{Root: securityDirectory}).Init(secadmin.InitOptions{InstanceID: "bridge-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-browser-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })

	var output bytes.Buffer
	command := exec.Command(binary, "-root_path", root, "-security_dir", securityDirectory,
		"-security_generation", initialized.Generation, "-runtime_dir", runtimeDirectory, "-websocket_addr", "127.0.0.1:0")
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-exited
		}
	})

	rootID, _ := locking.DirectoryObjectIdentity(root)
	discoveryKey, _ := localruntime.DiscoveryKey(rootID)
	var descriptor localruntime.EngineRuntimeDescriptor
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("engine exited before bridge readiness: %v\n%s", err, output.String())
		default:
		}
		descriptor, err = localruntime.LoadDescriptor(runtimeDirectory, discoveryKey)
		if err == nil && descriptor.State == localruntime.StateReady && descriptor.BrowserBridge != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descriptor.State != localruntime.StateReady || descriptor.BrowserBridge == nil {
		t.Fatalf("bridge endpoint not published: %#v, %v\n%s", descriptor, err, output.String())
	}
	client, err := browserbridge.DialEngineIPC(*descriptor.BrowserBridge, browserbridge.EngineIPCClientOptions{
		Generation: descriptor.Generation, Deliver: func(string, []byte) {}, Invalidate: func(string) {},
	})
	if err != nil {
		t.Fatalf("dial published bridge: %v", err)
	}
	defer client.Disconnect()
	response, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{
		Format: localruntime.LocalControlProtocol, Generation: descriptor.Generation, Operation: localruntime.OperationStop,
	})
	if err != nil || !response.OK {
		t.Fatalf("stop response = %#v, %v", response, err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("ordered engine stop: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-exited
		t.Fatalf("engine did not stop\n%s", output.String())
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("engine shutdown did not close the browser IPC connection")
	}
}
