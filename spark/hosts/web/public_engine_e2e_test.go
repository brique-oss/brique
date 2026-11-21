/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package webhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/localruntime"
	"brique_engine/engine/security/secadmin"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/shared"
	"brique_engine/platform/locking"

	"github.com/gorilla/websocket"
)

// This test deliberately crosses the process boundary. It proves that the
// verified publication, engine descriptor, public ingress and web host proxy
// use one compatible contract rather than only testing those pieces in
// isolation.
func TestPublishedRootRoundTripsAnIntentionThroughARealEngine(t *testing.T) {
	buildDirectory := t.TempDir()
	binary := filepath.Join(buildDirectory, "brique-engine")
	build := exec.Command("go", "build", "-o", binary, "brique_engine/cmd/brique")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build engine: %v\n%s", err, output)
	}

	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	contextJSON := []byte(`{
  "objective":{"name":"root","description":"public web end-to-end test"},
  "functional":{"role":"root"},"subjective":{},
  "brique":{"children_list":[],"ctx_ext_name":"root","ctx_name":"root","ctx_type":"root","ctx_version":"1",
	"ui_config":{"root_view":"RootView","adapter":"react","name":"main"},
    "engine_config":{"communication":{"websocket_listener":{"addr":"127.0.0.1:1"}},
      "execution":{},"reflexive":{},"trace":{}},"engine_version":"0.0.1","type":"local"}
}`)
	if err := os.WriteFile(filepath.Join(root, "context.json"), contextJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	securityDirectory := filepath.Join(t.TempDir(), "security")
	security := &secadmin.Dir{Root: securityDirectory}
	initialized, err := security.Init(secadmin.InitOptions{InstanceID: "public-web-e2e"})
	if err != nil {
		t.Fatal(err)
	}

	runtimeDirectory, err := os.MkdirTemp("/tmp", "brique-public-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	publicationDirectory := publishSparkPulseBrowserFixture(t, root)
	var output bytes.Buffer
	command := exec.Command(binary, "-root_path", root, "-security_dir", securityDirectory,
		"-security_generation", initialized.Generation, "-runtime_dir", runtimeDirectory,
		"-websocket_addr", "127.0.0.1:0", "-web_publication", publicationDirectory)
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

	rootObject, err := locking.DirectoryObjectIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	discoveryKey, err := localruntime.DiscoveryKey(rootObject)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := waitForPublicEngine(t, runtimeDirectory, discoveryKey, exited, &output)
	if err := security.PrincipalAdd(trust.PublicWebPrincipalID, trust.PrincipalKindPublic, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := security.GrantAdd(trust.PublicWebPrincipalID, "read", []string{"/root"}, nil); err != nil {
		t.Fatal(err)
	}
	applied, err := security.Apply(secadmin.ApplyOptions{Reload: true})
	if err != nil || !applied.Reloaded {
		t.Fatalf("apply public policy = %#v, %v", applied, err)
	}
	waitForActiveSecurityGeneration(t, security, applied.Generation, &output)

	server, err := Start(Config{Address: "127.0.0.1:0", PublicationDirectory: publicationDirectory,
		PublicWebBackend: descriptor.PublicWebSocket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	dialer := websocket.Dialer{Subprotocols: []string{"brique.public-web.v1"}}
	header := http.Header{"Origin": []string{strings.TrimSuffix(server.URL(), "/")}}
	const intentionID = "123e4567-e89b-42d3-a456-426614174000"
	request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: intentionID, AwaitResponse: true,
		To: circulation.Address{Context: shared.RootContextID, Type: "reflexive", Cap: "ui.list"},
	}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForPublicUIList(t, dialer, server.URL(), header, payload, &output)
	if got.Kind != circulation.ValueKindResponse || got.Response.IntentionID != intentionID ||
		got.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("public response = %#v\n%s", got, output.String())
	}
	result, ok := got.Response.Payload[circulation.KeyResult].([]any)
	if !ok || len(result) != 1 {
		t.Fatalf("ui.list result = %#v", got.Response.Payload[circulation.KeyResult])
	}
	verifyProxySeparatesObservedAddressQuotas(t, server.URL())
	for _, browser := range []struct {
		name       string
		executable string
	}{
		{name: "Chrome", executable: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		{name: "Brave", executable: "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"},
	} {
		browser := browser
		t.Run(browser.name, func(t *testing.T) {
			runChromiumPublicProbe(t, browser.name, browser.executable, server.URL())
		})
	}

	needles := privateKeyNeedles(t, securityDirectory)
	rootPage, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	rootBody, _ := io.ReadAll(io.LimitReader(rootPage.Body, 1<<20))
	_ = rootPage.Body.Close()
	assertNoPrivateKeyMaterial(t, "web host root page", rootBody, needles)
	assertNoPrivateKeyMaterial(t, "public response", mustMarshal(t, got), needles)
	filepath.WalkDir(publicationDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertNoPrivateKeyMaterial(t, "published file "+entry.Name(), content, needles)
		}
		return nil
	})

	stop, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{
		Format: localruntime.LocalControlProtocol, Generation: descriptor.Generation, Operation: localruntime.OperationStop,
	})
	if err != nil || !stop.OK {
		t.Fatalf("stop response = %#v, %v", stop, err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("ordered engine stop: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("engine did not stop\n%s", output.String())
	}
	// The engine has exited, so its captured output is complete and no longer written.
	assertNoPrivateKeyMaterial(t, "engine output", output.Bytes(), needles)
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// privateKeyNeedles returns every encoding of the private key material found in
// the machine security directory: the PEM body and the raw 32-byte Ed25519 seed
// as bytes, base64 (standard, URL, raw) and hex. T-29: none of them may appear in
// any output the instance produces.
func privateKeyNeedles(t *testing.T, securityDirectory string) [][]byte {
	t.Helper()
	var needles [][]byte
	found := 0
	_ = filepath.WalkDir(securityDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".pem") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(raw)
		if block == nil || !strings.Contains(block.Type, "PRIVATE KEY") || len(block.Bytes) < 32 {
			return nil
		}
		found++
		seed := block.Bytes[len(block.Bytes)-32:]
		body := strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(raw)),
			"-----BEGIN PRIVATE KEY-----"), "-----END PRIVATE KEY-----")), "")
		if !bytes.Contains(bytes.ReplaceAll(raw, []byte("\n"), nil), []byte(body)) {
			t.Fatal("positive control failed: the key body needle is not in its own file")
		}
		needles = append(needles, []byte(body), seed, []byte(hex.EncodeToString(seed)),
			[]byte(base64.StdEncoding.EncodeToString(seed)), []byte(base64.URLEncoding.EncodeToString(seed)),
			[]byte(base64.RawStdEncoding.EncodeToString(seed)), []byte(base64.RawURLEncoding.EncodeToString(seed)))
		return nil
	})
	if found < 2 {
		t.Fatalf("expected the message-signer and policy-authority private keys, found %d", found)
	}
	return needles
}

func assertNoPrivateKeyMaterial(t *testing.T, label string, content []byte, needles [][]byte) {
	t.Helper()
	for index, needle := range needles {
		if len(needle) >= 16 && bytes.Contains(content, needle) {
			t.Fatalf("%s contains private key material (needle %d)", label, index)
		}
	}
}

func verifyProxySeparatesObservedAddressQuotas(t *testing.T, publicURL string) {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(publicURL, "http") + "__brique/public/main"
	header := http.Header{
		"Origin":                    []string{strings.TrimSuffix(publicURL, "/")},
		"X-Brique-Observed-Address": []string{"203.0.113.99"},
		"X-Forwarded-For":           []string{"198.51.100.42"},
	}
	dialer := websocket.Dialer{Subprotocols: []string{"brique.public-web.v1"}}
	connections := make([]*websocket.Conn, 0, 8)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < 8; index++ {
		connection, response, err := dialer.Dial(endpoint, header)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			t.Fatalf("proxy quota connection %d: %v", index+1, err)
		}
		connections = append(connections, connection)
	}
	overflow, response, err := dialer.Dial(endpoint, header)
	if overflow != nil {
		_ = overflow.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ninth connection from one address: status=%v err=%v", responseStatusCode(response), err)
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
}

func waitForPublicUIList(t *testing.T, dialer websocket.Dialer, publicURL string, header http.Header, payload []byte, output *bytes.Buffer) circulation.Message {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(publicURL, "http") + "__brique/public/main"
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, response, err := dialer.Dial(endpoint, header)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err == nil {
			err = connection.WriteMessage(websocket.TextMessage, payload)
			if err == nil {
				_ = connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, responsePayload, readErr := connection.ReadMessage()
				err = readErr
				if readErr == nil {
					_ = connection.Close()
					var got circulation.Message
					if decodeErr := json.Unmarshal(responsePayload, &got); decodeErr != nil {
						t.Fatalf("decode public response: %v: %s", decodeErr, responsePayload)
					}
					return got
				}
			}
			_ = connection.Close()
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("public websocket did not adopt the reloaded policy: %v\n%s", lastErr, output.String())
	return circulation.Message{}
}

func publishSparkPulseBrowserFixture(t *testing.T, root string) string {
	t.Helper()
	const rootView = `import React from "react";
export function RootView({ pulse }) {
  React.useEffect(() => {
    let active = true;
    document.documentElement.dataset.briqueSparkPulseE2e = "running";
    pulse.emitUIIntentionAndWait({
      to: { context: "/root", type: "reflexive", cap: "ui.list" },
      sourcePathUI: "/probe",
      targetPathUI: "/probe"
    }).then((response) => {
      if (!active) return;
      const result = response?.payload?.result;
      document.documentElement.dataset.briqueSparkPulseE2e =
        response?.status === "ok" && Array.isArray(result) && result.length === 1 ? "ok" : "bad-response";
    }).catch((error) => {
      if (active) document.documentElement.dataset.briqueSparkPulseE2e = "error:" + String(error);
    });
    return () => { active = false; };
  }, [pulse]);
  return React.createElement("main", { id: "brique-spark-pulse-e2e" }, "Brique Spark Pulse E2E");
}
`
	source := filepath.Join(root, "RootView.tsx")
	if err := os.WriteFile(source, []byte(rootView), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	publicationDirectory := filepath.Join(t.TempDir(), "publication")
	command := exec.Command("node", "--experimental-strip-types", "--loader", "./spark/test/ts-resolve-loader.mjs",
		"./spark/test/publish-browser-e2e.ts", source, filepath.Join(root, "context.json"), publicationDirectory, repository)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("publish real Spark/Pulse browser fixture: %v\n%s", err, output)
	}
	return publicationDirectory
}

func runChromiumPublicProbe(t *testing.T, browserName, executable, publicURL string) {
	t.Helper()
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		t.Skipf("%s public browser probe skipped: executable is unavailable", browserName)
	}
	profile := filepath.Join(t.TempDir(), "browser-profile")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	debugPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profile, "--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port="+strconv.Itoa(debugPort), publicURL)
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stderr, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	defer func() {
		_ = command.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}()

	debuggerURL := waitForChromiumPage(t, browserName, debugPort, publicURL, exited, &stderr)
	connection, _, err := websocket.DefaultDialer.Dial(debuggerURL, nil)
	if err != nil {
		t.Fatalf("dial %s DevTools: %v\n%s", browserName, err, stderr.String())
	}
	defer connection.Close()
	for id := 1; id <= 100; id++ {
		request := map[string]any{"id": id, "method": "Runtime.evaluate", "params": map[string]any{
			"expression": "document.documentElement.dataset.briqueSparkPulseE2e || ''", "returnByValue": true,
		}}
		if err := connection.WriteJSON(request); err != nil {
			t.Fatal(err)
		}
		var response struct {
			ID     int `json:"id"`
			Result struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			} `json:"result"`
		}
		if err := connection.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID != id {
			id--
			continue
		}
		if response.Result.Result.Value == "ok" {
			return
		}
		if response.Result.Result.Value != "" && response.Result.Result.Value != "running" {
			t.Fatalf("%s public Brique probe = %q\n%s", browserName, response.Result.Result.Value, stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not complete the public Brique round-trip\n%s", browserName, stderr.String())
}

func waitForChromiumPage(t *testing.T, browserName string, debugPort int, publicURL string, exited <-chan error, output *bytes.Buffer) string {
	t.Helper()
	endpoint := "http://127.0.0.1:" + strconv.Itoa(debugPort) + "/json/list"
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("%s exited before DevTools readiness: %v\n%s", browserName, err, output.String())
		default:
		}
		response, err := client.Get(endpoint)
		if err == nil {
			var pages []struct {
				URL                  string `json:"url"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&pages)
			_ = response.Body.Close()
			if decodeErr == nil {
				for _, page := range pages {
					if page.URL == publicURL && page.WebSocketDebuggerURL != "" {
						return page.WebSocketDebuggerURL
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s DevTools page was not published\n%s", browserName, output.String())
	return ""
}

func waitForPublicEngine(t *testing.T, runtimeDirectory, discoveryKey string, exited <-chan error, output *bytes.Buffer) localruntime.EngineRuntimeDescriptor {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var descriptor localruntime.EngineRuntimeDescriptor
	var err error
	for time.Now().Before(deadline) {
		select {
		case processErr := <-exited:
			t.Fatalf("engine exited before public readiness: %v\n%s", processErr, output.String())
		default:
		}
		descriptor, err = localruntime.LoadDescriptor(runtimeDirectory, discoveryKey)
		if err == nil && descriptor.State == localruntime.StateReady && descriptor.PublicWebSocket != "" {
			return descriptor
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("engine never published public readiness: descriptor=%#v err=%v\n%s", descriptor, err, output.String())
	return localruntime.EngineRuntimeDescriptor{}
}

func waitForActiveSecurityGeneration(t *testing.T, security *secadmin.Dir, generation string, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var active *secadmin.Generation
	var err error
	for time.Now().Before(deadline) {
		active, err = security.ActiveGeneration()
		if err == nil && active != nil && active.Name == generation {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("security generation %q was not activated: active=%#v err=%v\n%s", generation, active, err, output.String())
}
