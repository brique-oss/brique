/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startTestWebUI(t *testing.T) (*WebUI, string) {
	t.Helper()
	directory := t.TempDir()
	ui, err := StartWebUI(WebUIOptions{Store: Store{Directory: directory}, DataDirectory: directory,
		ControlRuntimeDirectory: filepath.Join(directory, "control")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	return ui, directory
}

func webUIRequest(t *testing.T, ui *WebUI, method, path, token string, headers map[string]string, body string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+ui.listener.Addr().String()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		if key == "Host" {
			request.Host = value
		} else {
			request.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response, string(raw)
}

func TestWebUIServesThePageWithoutToken(t *testing.T) {
	ui, _ := startTestWebUI(t)
	response, body := webUIRequest(t, ui, "GET", "/", "", nil, "")
	if response.StatusCode != 200 || !strings.Contains(body, "Brique Launcher") {
		t.Fatalf("page: %d %q", response.StatusCode, body)
	}
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("missing CSP")
	}
}

func TestWebUIAPIRequiresTheToken(t *testing.T) {
	ui, _ := startTestWebUI(t)
	for _, token := range []string{"", "wrong"} {
		if response, _ := webUIRequest(t, ui, "GET", "/api/instances", token, nil, ""); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", token, response.StatusCode)
		}
	}
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	response, body := webUIRequest(t, ui, "GET", "/api/instances", token, nil, "")
	if response.StatusCode != 200 {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	var listing webUIInstances
	if err := json.Unmarshal([]byte(body), &listing); err != nil || listing.SupervisorUp || len(listing.Instances) != 0 {
		t.Fatalf("listing: %+v %v", listing, err)
	}
}

func TestWebUIRefusesForeignHostAndOrigin(t *testing.T) {
	ui, _ := startTestWebUI(t)
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	if response, _ := webUIRequest(t, ui, "GET", "/api/instances", token, map[string]string{"Host": "evil.example"}, ""); response.StatusCode != http.StatusForbidden {
		t.Fatalf("host: %d", response.StatusCode)
	}
	if response, _ := webUIRequest(t, ui, "GET", "/api/instances", token, map[string]string{"Origin": "http://evil.example"}, ""); response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin: %d", response.StatusCode)
	}
}

func TestWebUIWritesNeedJSONAndValidNames(t *testing.T) {
	ui, _ := startTestWebUI(t)
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	if response, _ := webUIRequest(t, ui, "POST", "/api/diagnose", token, map[string]string{"Content-Type": "text/plain"}, "{}"); response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("content type: %d", response.StatusCode)
	}
	json := map[string]string{"Content-Type": "application/json"}
	if response, _ := webUIRequest(t, ui, "POST", "/api/diagnose", token, json, `{"address":"has space.example"}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("address with a space: %d", response.StatusCode)
	}
	if response, _ := webUIRequest(t, ui, "POST", "/api/diagnose", token, json, `{"address":""}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty address: %d", response.StatusCode)
	}
	if response, _ := webUIRequest(t, ui, "POST", "/api/instances/..%2Fx/start", token, json, `{}`); response.StatusCode == 200 {
		t.Fatal("path alias accepted")
	}
	if response, _ := webUIRequest(t, ui, "POST", "/api/instances/a/delete", token, json, `{}`); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action: %d", response.StatusCode)
	}
}

func TestWebUIListenOnlyOnLoopback(t *testing.T) {
	if _, err := StartWebUI(WebUIOptions{DataDirectory: t.TempDir(), Address: "0.0.0.0:0"}); err == nil {
		t.Fatal("public address accepted")
	}
}

func TestWebUILogsAreRedactedAndBounded(t *testing.T) {
	ui, directory := startTestWebUI(t)
	token := strings.SplitN(ui.URL(), "#token=", 2)[1]
	logDirectory := filepath.Join(directory, "logs", "demo")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"token":"s3cret"}` + "\n" + strings.Repeat("x\n", 1000)
	if err := os.WriteFile(filepath.Join(logDirectory, "20260101T000000.000000000Z.log"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := webUIRequest(t, ui, "GET", "/api/instances/demo/logs", token, nil, "")
	var logs webUILogs
	if err := json.Unmarshal([]byte(body), &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs.Lines) != webUILogMaxRows || strings.Contains(body, "s3cret") {
		t.Fatalf("lines=%d", len(logs.Lines))
	}
	_, body = webUIRequest(t, ui, "GET", "/api/instances/none/logs", token, nil, "")
	if !strings.Contains(body, `"lines":[]`) {
		t.Fatalf("no log: %s", body)
	}
}

func TestWebUITokenIsDurableAndPrivate(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreateWebUIToken(directory)
	if err != nil || len(first) < 32 {
		t.Fatalf("token: %q %v", first, err)
	}
	second, err := LoadOrCreateWebUIToken(directory)
	if err != nil || second != first {
		t.Fatalf("token changed: %q %q %v", first, second, err)
	}
	info, err := os.Stat(filepath.Join(directory, webUITokenFile))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token file is readable by others: %v %v", info, err)
	}
	if err := PublishWebUIAddress(directory, "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	address, token, err := ReadWebUIAddress(directory)
	if err != nil || address != "127.0.0.1:1" || token != first {
		t.Fatalf("read: %q %q %v", address, token, err)
	}
	RetractWebUIAddress(directory)
	if _, _, err := ReadWebUIAddress(directory); err == nil {
		t.Fatal("address still published")
	}
}

func TestWebUIRefusesAShortToken(t *testing.T) {
	if _, err := StartWebUI(WebUIOptions{DataDirectory: t.TempDir(), Token: "short"}); err == nil {
		t.Fatal("short token accepted")
	}
}

func TestWebUIShutdownAsksTheSupervisorToStop(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine, Supervision: SupervisionExternal}); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: shortControlDirectory(t)})
	if err != nil {
		t.Fatal(err)
	}
	controlRuntime := shortControlDirectory(t)
	control, err := StartLauncherControl(supervisor, controlRuntime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	ui, err := StartWebUI(WebUIOptions{Store: store, DataDirectory: store.Directory, ControlRuntimeDirectory: controlRuntime, Token: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Close() })
	if response, _ := webUIRequest(t, ui, "POST", "/api/shutdown", "", nil, "{}"); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("shutdown without the token = %d", response.StatusCode)
	}
	select {
	case <-control.ShutdownRequested():
		t.Fatal("the supervisor was asked to stop by a request without the token")
	default:
	}
	response, body := webUIRequest(t, ui, "POST", "/api/shutdown", strings.Repeat("a", 40), map[string]string{"Content-Type": "application/json"}, "{}")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"stopping"`) {
		t.Fatalf("shutdown = %d %s", response.StatusCode, body)
	}
	select {
	case <-control.ShutdownRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor was not asked to stop")
	}
}

func TestWebUIServesItsIconWithoutToken(t *testing.T) {
	ui, _ := startTestWebUI(t)
	response, body := webUIRequest(t, ui, "GET", "/favicon.svg", "", nil, "")
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "image/svg+xml") || !strings.Contains(body, "<svg") {
		t.Fatalf("icon: %d %q %.80q", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
	if _, page := webUIRequest(t, ui, "GET", "/", "", nil, ""); !strings.Contains(page, `rel="icon"`) {
		t.Fatal("the page does not link its icon")
	}
}
