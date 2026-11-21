/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"brique_engine/browserbridge"
	"brique_engine/launcher"
	"brique_engine/localruntime"
	"brique_engine/shared"
)

type hostTestSink struct {
	mu       sync.Mutex
	opened   map[string]browserbridge.Binding
	messages map[string][][]byte
	closed   map[string]int
}

func newHostTestSink() *hostTestSink {
	return &hostTestSink{opened: make(map[string]browserbridge.Binding), messages: make(map[string][][]byte),
		closed: make(map[string]int)}
}

func (sink *hostTestSink) Open(binding browserbridge.Binding) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.opened[binding.ChannelID] = binding
	return nil
}

func (sink *hostTestSink) Message(binding browserbridge.Binding, payload []byte) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.messages[binding.ChannelID] = append(sink.messages[binding.ChannelID], append([]byte(nil), payload...))
	return nil
}

func (sink *hostTestSink) Close(binding browserbridge.Binding) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed[binding.ChannelID]++
}

func TestLocalOriginSinkRequiresTheSelectedSupervisedWebOrigin(t *testing.T) {
	next := newHostTestSink()
	gate := &localOriginSink{next: next, resolve: func() (string, error) {
		return "http://127.0.0.1:4123", nil
	}}
	local := browserbridge.Binding{ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Origin: "http://127.0.0.1:4123", UIName: "root"}
	if err := gate.Open(local); err != nil {
		t.Fatal(err)
	}
	wrong := local
	wrong.ChannelID = "bbc1_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	wrong.Origin = "http://127.0.0.1:4999"
	if err := gate.Open(wrong); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("arbitrary loopback origin error = %v", err)
	}
	unavailable := &localOriginSink{next: next, resolve: func() (string, error) {
		return "", errors.New("launcher unavailable")
	}}
	if err := unavailable.Open(local); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("unavailable supervisor error = %v", err)
	}
	remote := wrong
	remote.Origin = "https://remote.example"
	remote.Descriptor = []byte("signed descriptor")
	if err := gate.Open(remote); err != nil {
		t.Fatalf("remote descriptor was subjected to the local origin gate: %v", err)
	}
}

func TestCanonicalSupervisedWebOriginAcceptsOnlyTheRootURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:4123", "http://127.0.0.1:4123/"} {
		if origin, err := canonicalSupervisedWebOrigin(raw); err != nil || origin != "http://127.0.0.1:4123" {
			t.Fatalf("canonical origin for %q = %q, %v", raw, origin, err)
		}
	}
	for _, raw := range []string{
		"https://127.0.0.1:4123/", "http://user@127.0.0.1:4123/", "http://127.0.0.1:4123/ui",
		"http://127.0.0.1:4123//", "http://127.0.0.1:4123/?x=1", "http://127.0.0.1:4123/#x",
	} {
		if _, err := canonicalSupervisedWebOrigin(raw); !errors.Is(err, ErrHostUnavailable) {
			t.Fatalf("non-root supervised URL %q accepted: %v", raw, err)
		}
	}
}

func TestNativeHostBridgesSelectedRunningInstanceWithoutKeys(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	launcherDirectory := filepath.Join(t.TempDir(), "launcher")
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-host-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDirectory)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := launcher.Store{Directory: launcherDirectory,
		Inspect: func(context.Context, string) (shared.BuildInfo, error) { return shared.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	gateway, err := store.SetBrowserGateway("alpha")
	if err != nil {
		t.Fatal(err)
	}
	rootObject, _ := shared.DirectoryObjectIdentity(root)
	generation, _ := localruntime.NewGeneration()
	control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: runtimeDirectory,
		RootPath: root, RootObject: rootObject, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	sink := newHostTestSink()
	listener, err := browserbridge.StartEngineIPCListener(runtimeDirectory, control.Descriptor().DiscoveryKey, generation, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := control.SetBrowserBridgeEndpoint(listener.Endpoint()); err != nil {
		t.Fatal(err)
	}
	if err := control.SetState(localruntime.StateReady); err != nil {
		t.Fatal(err)
	}

	hostInput, extensionOutput := io.Pipe()
	extensionInput, hostOutput := io.Pipe()
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- Run(Options{Input: hostInput, Output: hostOutput,
			LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory})
	}()
	defer extensionOutput.Close()
	defer extensionInput.Close()

	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1,
	})
	var hello browserbridge.Response
	readHostValue(t, extensionInput, &hello)
	if !hello.OK || hello.SessionID == "" || hello.GatewayAlias != "alpha" || hello.GatewayRevision != gateway.Revision {
		t.Fatalf("hello = %+v", hello)
	}
	descriptor := base64.RawURLEncoding.EncodeToString([]byte("remote descriptor"))
	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2, "session_id": hello.SessionID,
		"origin": "https://ui.example", "document_id": "document-1", "frame_id": 0,
		"ui_name": "root", "user_activated": true, "descriptor_b64": descriptor,
	})
	var opened browserbridge.Response
	readHostValue(t, extensionInput, &opened)
	if !opened.OK || opened.ChannelID == "" {
		t.Fatalf("open = %+v", opened)
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte("intention"))
	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "message", "sequence": 3,
		"session_id": hello.SessionID, "channel_id": opened.ChannelID, "payload_b64": payload,
	})
	var accepted browserbridge.Response
	readHostValue(t, extensionInput, &accepted)
	if !accepted.OK {
		t.Fatalf("message = %+v", accepted)
	}
	if err := listener.Server().Deliver(opened.ChannelID, []byte("response")); err != nil {
		t.Fatal(err)
	}
	var event browserbridge.Event
	readHostValue(t, extensionInput, &event)
	decoded, _ := base64.RawURLEncoding.DecodeString(event.PayloadB64)
	if event.ChannelID != opened.ChannelID || string(decoded) != "response" {
		t.Fatalf("event = %+v", event)
	}
	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "disconnect", "sequence": 4, "session_id": hello.SessionID,
	})
	var disconnected browserbridge.Response
	readHostValue(t, extensionInput, &disconnected)
	if !disconnected.OK || disconnected.Kind != "disconnect" {
		t.Fatalf("disconnect = %+v", disconnected)
	}
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native host did not stop after disconnect")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.opened) != 1 || len(sink.messages[opened.ChannelID]) != 1 ||
		string(sink.messages[opened.ChannelID][0]) != "intention" || sink.closed[opened.ChannelID] != 1 {
		t.Fatalf("sink state: opened=%d messages=%q closed=%d", len(sink.opened), sink.messages, sink.closed[opened.ChannelID])
	}
}

func TestNativeHostGatewayChangeAppliesOnlyToNewSessions(t *testing.T) {
	launcherDirectory := filepath.Join(t.TempDir(), "launcher")
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-host-gateway-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDirectory)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := launcher.Store{Directory: launcherDirectory,
		Inspect: func(context.Context, string) (shared.BuildInfo, error) { return shared.CurrentBuildInfo(), nil }}

	startEngine := func(alias string) (*hostTestSink, *localruntime.ControlServer, *browserbridge.EngineIPCListener) {
		t.Helper()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: alias, RootPath: root,
			EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
			t.Fatal(err)
		}
		rootObject, err := shared.DirectoryObjectIdentity(root)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := localruntime.NewGeneration()
		if err != nil {
			t.Fatal(err)
		}
		control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: runtimeDirectory,
			RootPath: root, RootObject: rootObject, Generation: generation})
		if err != nil {
			t.Fatal(err)
		}
		sink := newHostTestSink()
		listener, err := browserbridge.StartEngineIPCListener(runtimeDirectory, control.Descriptor().DiscoveryKey,
			generation, sink)
		if err != nil {
			control.Close()
			t.Fatal(err)
		}
		if err := control.SetBrowserBridgeEndpoint(listener.Endpoint()); err != nil {
			listener.Close()
			control.Close()
			t.Fatal(err)
		}
		if err := control.SetState(localruntime.StateReady); err != nil {
			listener.Close()
			control.Close()
			t.Fatal(err)
		}
		return sink, control, listener
	}
	alphaSink, alphaControl, alphaListener := startEngine("alpha")
	defer alphaControl.Close()
	defer alphaListener.Close()
	betaSink, betaControl, betaListener := startEngine("beta")
	defer betaControl.Close()
	defer betaListener.Close()

	type runningHost struct {
		input  *io.PipeWriter
		output *io.PipeReader
		done   chan error
	}
	startHost := func() runningHost {
		t.Helper()
		hostInput, extensionOutput := io.Pipe()
		extensionInput, hostOutput := io.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- Run(Options{Input: hostInput, Output: hostOutput,
				LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory})
		}()
		return runningHost{input: extensionOutput, output: extensionInput, done: done}
	}
	openHost := func(host runningHost, wantAlias string) (browserbridge.Response, browserbridge.Response) {
		t.Helper()
		writeHostRequest(t, host.input, map[string]any{
			"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1,
		})
		var hello browserbridge.Response
		readHostValue(t, host.output, &hello)
		if !hello.OK || hello.GatewayAlias != wantAlias {
			t.Fatalf("%s hello = %+v", wantAlias, hello)
		}
		writeHostRequest(t, host.input, map[string]any{
			"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2,
			"session_id": hello.SessionID, "origin": "https://ui.example", "document_id": "document-1",
			"frame_id": 0, "ui_name": "root", "user_activated": true,
			"descriptor_b64": base64.RawURLEncoding.EncodeToString([]byte("remote descriptor")),
		})
		var opened browserbridge.Response
		readHostValue(t, host.output, &opened)
		if !opened.OK || opened.ChannelID == "" {
			t.Fatalf("%s open = %+v", wantAlias, opened)
		}
		return hello, opened
	}
	send := func(host runningHost, hello, opened browserbridge.Response, sequence uint64, payload string) {
		t.Helper()
		writeHostRequest(t, host.input, map[string]any{
			"format": browserbridge.BridgeProtocol, "kind": "message", "sequence": sequence,
			"session_id": hello.SessionID, "channel_id": opened.ChannelID,
			"payload_b64": base64.RawURLEncoding.EncodeToString([]byte(payload)),
		})
		var response browserbridge.Response
		readHostValue(t, host.output, &response)
		if !response.OK {
			t.Fatalf("send %q = %+v", payload, response)
		}
	}
	disconnect := func(host runningHost, hello browserbridge.Response, sequence uint64) {
		t.Helper()
		writeHostRequest(t, host.input, map[string]any{
			"format": browserbridge.BridgeProtocol, "kind": "disconnect", "sequence": sequence,
			"session_id": hello.SessionID,
		})
		var response browserbridge.Response
		readHostValue(t, host.output, &response)
		if !response.OK {
			t.Fatalf("disconnect = %+v", response)
		}
		select {
		case err := <-host.done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("native host did not stop")
		}
		host.input.Close()
		host.output.Close()
	}

	firstGateway, err := store.SetBrowserGateway("alpha")
	if err != nil {
		t.Fatal(err)
	}
	alphaHost := startHost()
	alphaHello, alphaOpen := openHost(alphaHost, "alpha")
	if alphaHello.GatewayRevision != firstGateway.Revision {
		t.Fatalf("alpha revision = %d, want %d", alphaHello.GatewayRevision, firstGateway.Revision)
	}

	secondGateway, err := store.SetBrowserGateway("beta")
	if err != nil {
		t.Fatal(err)
	}
	send(alphaHost, alphaHello, alphaOpen, 3, "stays-alpha")
	betaHost := startHost()
	betaHello, betaOpen := openHost(betaHost, "beta")
	if betaHello.GatewayRevision != secondGateway.Revision {
		t.Fatalf("beta revision = %d, want %d", betaHello.GatewayRevision, secondGateway.Revision)
	}
	send(betaHost, betaHello, betaOpen, 3, "goes-beta")

	alphaSink.mu.Lock()
	alphaMessages := alphaSink.messages[alphaOpen.ChannelID]
	alphaSink.mu.Unlock()
	betaSink.mu.Lock()
	betaMessages := betaSink.messages[betaOpen.ChannelID]
	betaSink.mu.Unlock()
	if len(alphaMessages) != 1 || string(alphaMessages[0]) != "stays-alpha" ||
		len(betaMessages) != 1 || string(betaMessages[0]) != "goes-beta" {
		t.Fatalf("gateway moved an active channel: alpha=%q beta=%q", alphaMessages, betaMessages)
	}

	disconnect(alphaHost, alphaHello, 4)
	disconnect(betaHost, betaHello, 4)
	if err := betaListener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := betaControl.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Run(Options{Input: io.NopCloser(bytes.NewReader(nil)), Output: io.Discard,
		LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory}); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("host with unavailable selected gateway = %v", err)
	}
	if _, err := store.ClearBrowserGateway(); err != nil {
		t.Fatal(err)
	}
	if err := Run(Options{Input: io.NopCloser(bytes.NewReader(nil)), Output: io.Discard,
		LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory}); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("host without selected gateway = %v", err)
	}
}

func TestNativeHostStopsWhenEngineIPCDisappearsWhileBrowserIsIdle(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	launcherDirectory := filepath.Join(t.TempDir(), "launcher")
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-host-idle-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDirectory)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := launcher.Store{Directory: launcherDirectory,
		Inspect: func(context.Context, string) (shared.BuildInfo, error) { return shared.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBrowserGateway("alpha"); err != nil {
		t.Fatal(err)
	}
	rootObject, _ := shared.DirectoryObjectIdentity(root)
	generation, _ := localruntime.NewGeneration()
	control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: runtimeDirectory,
		RootPath: root, RootObject: rootObject, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	listener, err := browserbridge.StartEngineIPCListener(runtimeDirectory, control.Descriptor().DiscoveryKey,
		generation, newHostTestSink())
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SetBrowserBridgeEndpoint(listener.Endpoint()); err != nil {
		t.Fatal(err)
	}
	if err := control.SetState(localruntime.StateReady); err != nil {
		t.Fatal(err)
	}

	hostInput, extensionOutput := io.Pipe()
	extensionInput, hostOutput := io.Pipe()
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- Run(Options{Input: hostInput, Output: hostOutput,
			LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory})
	}()
	defer extensionOutput.Close()
	defer extensionInput.Close()

	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1,
	})
	var hello browserbridge.Response
	readHostValue(t, extensionInput, &hello)
	if !hello.OK {
		t.Fatalf("hello = %+v", hello)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-hostDone:
		if !errors.Is(err, ErrHostUnavailable) {
			t.Fatalf("Run error = %v, want %v", err, ErrHostUnavailable)
		}
	case <-time.After(time.Second):
		t.Fatal("native host remained blocked on browser input after engine IPC closed")
	}
}

func TestNativeHostRefusesBridgeFromAnotherGeneration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launcherDirectory := filepath.Join(t.TempDir(), "launcher")
	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-host-fake-bridge-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDirectory)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := launcher.Store{Directory: launcherDirectory,
		Inspect: func(context.Context, string) (shared.BuildInfo, error) { return shared.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBrowserGateway("alpha"); err != nil {
		t.Fatal(err)
	}
	rootObject, err := shared.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	engineGeneration, err := localruntime.NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	foreignGeneration, err := localruntime.NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: runtimeDirectory,
		RootPath: root, RootObject: rootObject, Generation: engineGeneration})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	foreignSink := newHostTestSink()
	listener, err := browserbridge.StartEngineIPCListener(runtimeDirectory, control.Descriptor().DiscoveryKey,
		foreignGeneration, foreignSink)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := control.SetBrowserBridgeEndpoint(listener.Endpoint()); err != nil {
		t.Fatal(err)
	}
	if err := control.SetState(localruntime.StateReady); err != nil {
		t.Fatal(err)
	}

	err = Run(Options{Input: io.NopCloser(bytes.NewReader(nil)), Output: io.Discard,
		LauncherDirectory: launcherDirectory, RuntimeDirectory: runtimeDirectory})
	if !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("host accepted a bridge from another generation: %v", err)
	}
	foreignSink.mu.Lock()
	defer foreignSink.mu.Unlock()
	if len(foreignSink.opened) != 0 || len(foreignSink.messages) != 0 {
		t.Fatalf("foreign generation reached sink: opened=%d messages=%d",
			len(foreignSink.opened), len(foreignSink.messages))
	}
}

func writeHostRequest(t *testing.T, destination io.Writer, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("write native request: %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- browserbridge.WriteFrame(destination, raw) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("write native request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native request write timed out")
	}
}

func readHostValue(t *testing.T, source io.Reader, destination any) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		raw, err := browserbridge.ReadFrame(source)
		if err == nil {
			err = json.Unmarshal(raw, destination)
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("read native response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native response timed out")
	}
}
