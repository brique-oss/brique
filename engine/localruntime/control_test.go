package localruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/engine/browserbridge"
	"brique_engine/platform/locking"
	"brique_engine/platform/protectedfile"
)

func startTestControl(t *testing.T) (*ControlServer, EngineRuntimeDescriptor) {
	t.Helper()
	root := t.TempDir()
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	id, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	server, err := StartControl(ControlOptions{
		DiscoveryDirectory: runtimeDirectory, RootPath: root,
		RootObject: id, Generation: generation, Now: func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, server.Descriptor()
}

func callTestControl(t *testing.T, descriptor EngineRuntimeDescriptor, operation string) ControlResponse {
	t.Helper()
	response, err := CallControl(descriptor, ControlRequest{Format: LocalControlProtocol, Generation: descriptor.Generation, Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestControlPublishesAndServesItsBoundGeneration(t *testing.T) {
	server, descriptor := startTestControl(t)
	loaded, err := LoadDescriptor(server.directory, descriptor.DiscoveryKey)
	if err != nil || loaded.Generation != descriptor.Generation || loaded.Control != descriptor.Control {
		t.Fatalf("published descriptor = %#v, %v", loaded, err)
	}

	status := callTestControl(t, descriptor, OperationStatus)
	if !status.OK || status.State != StateStarting || status.PID != os.Getpid() {
		t.Fatalf("status = %#v", status)
	}
	version := callTestControl(t, descriptor, OperationVersion)
	if !version.OK || version.Build == nil || !version.Build.Valid() {
		t.Fatalf("version = %#v", version)
	}
	root := callTestControl(t, descriptor, OperationRoot)
	if !root.OK || root.RootPath != descriptor.RootPath || root.RootObject == nil || *root.RootObject != descriptor.RootObject {
		t.Fatalf("root = %#v", root)
	}
	if generation := callTestControl(t, descriptor, OperationGeneration); !generation.OK || generation.Generation != descriptor.Generation {
		t.Fatalf("generation = %#v", generation)
	}

	if err := server.SetState(StateReady); err != nil {
		t.Fatal(err)
	}
	descriptor = server.Descriptor()
	if status := callTestControl(t, descriptor, OperationStatus); !status.OK || status.State != StateReady {
		t.Fatalf("ready status = %#v", status)
	}
}

func TestControlPublishesBrowserBridgeEndpointAtomically(t *testing.T) {
	server, descriptor := startTestControl(t)
	endpoint := browserbridge.EngineIPCEndpoint{Protocol: browserbridge.EngineIPCProtocol,
		Transport: browserbridge.IPCTransportUnixSocket, Address: filepath.Join(server.directory, "browser", "bridge.sock")}
	if descriptor.Control.Transport == TransportWindowsNamedPipe {
		endpoint.Transport = browserbridge.IPCTransportWindowsNamedPipe
		endpoint.Address = `\\.\pipe\Brique-browser-test`
	}
	if err := server.SetBrowserBridgeEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDescriptor(server.directory, descriptor.DiscoveryKey)
	if err != nil || loaded.BrowserBridge == nil || *loaded.BrowserBridge != endpoint {
		t.Fatalf("browser bridge endpoint = %#v, %v", loaded.BrowserBridge, err)
	}
}

func TestControlPublishesPublicWebSocketEndpointAtomically(t *testing.T) {
	server, descriptor := startTestControl(t)
	const endpoint = "ws://127.0.0.1:43210/__brique/public/"
	if err := server.SetPublicWebSocketAddress(endpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDescriptor(server.directory, descriptor.DiscoveryKey)
	if err != nil || loaded.PublicWebSocket != endpoint {
		t.Fatalf("public web endpoint = %q, %v", loaded.PublicWebSocket, err)
	}
	if err := server.SetPublicWebSocketAddress("ws://public.example/__brique/public/"); err == nil {
		t.Fatal("non-loopback public backend was published in local discovery")
	}
}

func TestControlRejectsStaleGenerationAndRequestsOrderedStop(t *testing.T) {
	server, descriptor := startTestControl(t)
	stale := descriptor
	stale.Generation, _ = NewGeneration()
	response, err := CallControl(descriptor, ControlRequest{Format: LocalControlProtocol, Generation: stale.Generation, Operation: OperationStatus})
	if err != nil || response.OK || response.ErrorCode != "stale_generation" {
		t.Fatalf("stale response = %#v, %v", response, err)
	}
	unknown := callTestControl(t, descriptor, "delete_everything")
	if unknown.OK || unknown.ErrorCode != "unknown_operation" {
		t.Fatalf("unknown response = %#v", unknown)
	}
	stop := callTestControl(t, descriptor, OperationStop)
	if !stop.OK {
		t.Fatalf("stop response = %#v", stop)
	}
	select {
	case <-server.StopRequested():
	case <-time.After(time.Second):
		t.Fatal("stop request was not delivered")
	}
}

func TestControlCloseRemovesOnlyItsDescriptorAndEndpoint(t *testing.T) {
	server, descriptor := startTestControl(t)
	endpoint := descriptor.Control.Address
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDescriptor(server.directory, descriptor.DiscoveryKey); err == nil {
		t.Fatal("descriptor remained after close")
	}
	if descriptor.Control.Transport == TransportUnixSocket {
		if _, err := os.Lstat(endpoint); !os.IsNotExist(err) {
			t.Fatalf("control socket remained: %v", err)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestDiscoveryDirectoryRequiresAnAbsoluteProtectedPath(t *testing.T) {
	if _, err := ResolveDiscoveryDirectory("relative"); err == nil {
		t.Fatal("relative runtime directory accepted")
	}
	directory, err := ResolveDiscoveryDirectory(t.TempDir())
	if err != nil || !filepath.IsAbs(directory) {
		t.Fatalf("runtime directory = %q, %v", directory, err)
	}
	if err := PrepareDiscoveryDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := protectedfile.ValidateProtectedDirectory(directory, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOwnedDescriptorStillRefusesAnotherGeneration(t *testing.T) {
	server, descriptor := startTestControl(t)
	other, _ := NewGeneration()
	if err := RemoveOwnedDescriptor(server.directory, descriptor.DiscoveryKey, other); !errors.Is(err, ErrDescriptorNotOwned) {
		t.Fatalf("foreign remove = %v", err)
	}
}
