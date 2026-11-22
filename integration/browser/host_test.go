/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browser_test

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

	"brique_engine/contracts/buildinfo"
	"brique_engine/engine/browserbridge"
	"brique_engine/engine/enginebuild"
	"brique_engine/engine/localruntime"
	"brique_engine/launcher"
	"brique_engine/platform/locking"
	"brique_engine/spark/hosts/browser/host"
)

func newResolver(t *testing.T, directory string) browserhost.InstanceResolver {
	t.Helper()
	resolver, err := launcher.NewInstanceResolver(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

// testExtensionID is the extension these tests pretend started the native host.
const testExtensionID = "abcdefghijklmnopabcdefghijklmnop"

type gatewayChoice struct{ Revision uint64 }

// setGateway makes the instance the one the owner associated with the extension (its identity too): it writes the association file of
// the launcher, which the host reads when it starts. A new revision is a new choice; hosts already running keep the one they started with.
func setGateway(store launcher.Store, alias string) (gatewayChoice, error) {
	return writeAssociations(store, []launcher.BrowserAssociation{{Browser: "chrome", ExtensionID: testExtensionID, Instances: []string{alias}, Identity: alias}})
}

func clearGateway(store launcher.Store) (gatewayChoice, error) {
	return writeAssociations(store, nil)
}

func writeAssociations(store launcher.Store, entries []launcher.BrowserAssociation) (gatewayChoice, error) {
	current, err := store.BrowserAssociations()
	if err != nil {
		return gatewayChoice{}, err
	}
	next := launcher.BrowserAssociations{Format: current.Format, Revision: current.Revision + 1, Entries: entries}
	if next.Format == "" {
		next.Format = "brique.browser-associations/1"
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return gatewayChoice{}, err
	}
	directory := filepath.Join(store.Directory, "browser")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return gatewayChoice{}, err
	}
	if err := os.WriteFile(filepath.Join(directory, "associations.json"), encoded, 0o600); err != nil {
		return gatewayChoice{}, err
	}
	return gatewayChoice{Revision: next.Revision}, nil
}

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
		Inspect: func(context.Context, string) (buildinfo.Info, error) { return enginebuild.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	gateway, err := setGateway(store, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	rootObject, _ := locking.DirectoryObjectIdentity(root)
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
		hostDone <- browserhost.Run(browserhost.Options{Input: hostInput, Output: hostOutput,
			Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory})
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
		"ui_name": "root", "descriptor_b64": descriptor,
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
		Inspect: func(context.Context, string) (buildinfo.Info, error) { return enginebuild.CurrentBuildInfo(), nil }}

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
		rootObject, err := locking.DirectoryObjectIdentity(root)
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
			done <- browserhost.Run(browserhost.Options{Input: hostInput, Output: hostOutput,
				Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory})
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
			"frame_id": 0, "ui_name": "root",
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

	firstGateway, err := setGateway(store, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	alphaHost := startHost()
	alphaHello, alphaOpen := openHost(alphaHost, "alpha")
	if alphaHello.GatewayRevision != firstGateway.Revision {
		t.Fatalf("alpha revision = %d, want %d", alphaHello.GatewayRevision, firstGateway.Revision)
	}

	secondGateway, err := setGateway(store, "beta")
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
	// The engine the owner chose is gone: the host starts (it dials an engine when a page opens) and refuses the page.
	staleInput, staleExtensionOutput := io.Pipe()
	staleExtensionInput, staleOutput := io.Pipe()
	staleDone := make(chan error, 1)
	go func() {
		staleDone <- browserhost.Run(browserhost.Options{Input: staleInput, Output: staleOutput,
			Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory})
	}()
	writeHostRequest(t, staleExtensionOutput, map[string]any{"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1})
	var staleHello browserbridge.Response
	readHostValue(t, staleExtensionInput, &staleHello)
	writeHostRequest(t, staleExtensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2, "session_id": staleHello.SessionID,
		"origin": "https://ui.example", "document_id": "document-1", "frame_id": 0, "ui_name": "root",
		"descriptor_b64": base64.RawURLEncoding.EncodeToString([]byte("remote descriptor")),
	})
	var staleOpen browserbridge.Response
	readHostValue(t, staleExtensionInput, &staleOpen)
	if !staleHello.OK || staleOpen.OK {
		t.Fatalf("host with unavailable selected gateway: hello=%+v open=%+v", staleHello, staleOpen)
	}
	_ = staleExtensionOutput.Close()
	_ = staleExtensionInput.Close()
	select {
	case <-staleDone:
	case <-time.After(time.Second):
		t.Fatal("the host did not stop")
	}
	if _, err := clearGateway(store); err != nil {
		t.Fatal(err)
	}
	if err := browserhost.Run(browserhost.Options{Input: io.NopCloser(bytes.NewReader(nil)), Output: io.Discard,
		Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory}); !errors.Is(err, browserhost.ErrHostUnavailable) {
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
		Inspect: func(context.Context, string) (buildinfo.Info, error) { return enginebuild.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	if _, err := setGateway(store, "alpha"); err != nil {
		t.Fatal(err)
	}
	rootObject, _ := locking.DirectoryObjectIdentity(root)
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
		hostDone <- browserhost.Run(browserhost.Options{Input: hostInput, Output: hostOutput,
			Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory})
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
	// The host dials an engine when a page opens: a page that carries a descriptor goes to the identity instance.
	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2, "session_id": hello.SessionID,
		"origin": "https://ui.example", "document_id": "document-1", "frame_id": 0, "ui_name": "root",
		"descriptor_b64": base64.RawURLEncoding.EncodeToString([]byte("remote descriptor")),
	})
	var opened browserbridge.Response
	readHostValue(t, extensionInput, &opened)
	if !opened.OK {
		t.Fatalf("open = %+v", opened)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-hostDone:
		if !errors.Is(err, browserhost.ErrHostUnavailable) {
			t.Fatalf("Run error = %v, want %v", err, browserhost.ErrHostUnavailable)
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
		Inspect: func(context.Context, string) (buildinfo.Info, error) { return enginebuild.CurrentBuildInfo(), nil }}
	if _, err := store.Add(context.Background(), launcher.AddOptions{Alias: "alpha", RootPath: root,
		EnginePath: executable, Supervision: launcher.SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	if _, err := setGateway(store, "alpha"); err != nil {
		t.Fatal(err)
	}
	rootObject, err := locking.DirectoryObjectIdentity(root)
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

	hostInput, extensionOutput := io.Pipe()
	extensionInput, hostOutput := io.Pipe()
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- browserhost.Run(browserhost.Options{Input: hostInput, Output: hostOutput,
			Resolver: newResolver(t, launcherDirectory), ExtensionID: testExtensionID, RuntimeDirectory: runtimeDirectory})
	}()
	defer extensionOutput.Close()
	defer extensionInput.Close()
	writeHostRequest(t, extensionOutput, map[string]any{"format": browserbridge.BridgeProtocol, "kind": "hello", "sequence": 1})
	var hello browserbridge.Response
	readHostValue(t, extensionInput, &hello)
	if !hello.OK {
		t.Fatalf("hello = %+v", hello)
	}
	writeHostRequest(t, extensionOutput, map[string]any{
		"format": browserbridge.BridgeProtocol, "kind": "open", "sequence": 2, "session_id": hello.SessionID,
		"origin": "https://ui.example", "document_id": "document-1", "frame_id": 0, "ui_name": "root",
		"descriptor_b64": base64.RawURLEncoding.EncodeToString([]byte("remote descriptor")),
	})
	var opened browserbridge.Response
	readHostValue(t, extensionInput, &opened)
	if opened.OK {
		t.Fatalf("host accepted a bridge from another generation: %+v", opened)
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
