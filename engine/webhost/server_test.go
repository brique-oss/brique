/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package webhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testPublication(t testing.TB) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<!doctype html><script type=\"module\" src=\"/assets/app.abcdef12.js\"></script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.abcdef12.js"), []byte("export const ready = true;"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestPublicationManifest(t, directory)
	return directory
}

func TestWebHostProxiesPublicWebSocketWithoutOwningItsProtocol(t *testing.T) {
	observedAddresses := make(chan string, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{"brique.public-web.v1"}, CheckOrigin: func(request *http.Request) bool {
		return request.Header.Get("Origin") == "http://"+request.Host
	}}
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedAddresses <- request.Header.Get(publicWebObservedAddressHeader)
		if request.URL.Path != "/__brique/public/main" {
			http.NotFound(writer, request)
			return
		}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		kind, payload, err := connection.ReadMessage()
		if err == nil {
			_ = connection.WriteMessage(kind, payload)
		}
	}))
	defer backend.Close()
	backendWS := "ws" + strings.TrimPrefix(backend.URL, "http") + "/__brique/public/"
	server, err := Start(Config{Address: "127.0.0.1:0", PublicationDirectory: testPublication(t), PublicWebBackend: backendWS})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	page, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	csp := page.Header.Get("Content-Security-Policy")
	_ = page.Body.Close()
	exactWebSocketOrigin := "ws" + strings.TrimPrefix(strings.TrimSuffix(server.URL(), "/"), "http")
	if !strings.Contains(csp, "connect-src 'self' "+exactWebSocketOrigin+";") || strings.Contains(csp, "connect-src 'self' ws:;") {
		t.Fatalf("public web CSP = %q", csp)
	}
	dialer := websocket.Dialer{Subprotocols: []string{"brique.public-web.v1"}}
	header := http.Header{"Origin": []string{strings.TrimSuffix(server.URL(), "/")},
		publicWebObservedAddressHeader: []string{"203.0.113.99"},
		publicWebClientAddressHeader:   []string{"192.0.2.55"},
		"X-Forwarded-For":              []string{"198.51.100.42"}}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL(), "http")+"__brique/public/main", header)
	if err != nil {
		t.Fatalf("proxy dial: status=%v err=%v", responseStatusCode(response), err)
	}
	defer connection.Close()
	if observed := <-observedAddresses; observed != "127.0.0.1" {
		t.Fatalf("proxy observed address = %q", observed)
	}
	if connection.Subprotocol() != "brique.public-web.v1" {
		t.Fatalf("subprotocol = %q", connection.Subprotocol())
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte("intent")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.ReadMessage()
	if err != nil || string(payload) != "intent" {
		t.Fatalf("proxy echo = %q, %v", payload, err)
	}
	connection.Close()

	badHeader := http.Header{"Origin": []string{"http://evil.example"}}
	bad, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL(), "http")+"__brique/public/main", badHeader)
	if bad != nil {
		bad.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: status=%v err=%v", responseStatusCode(response), err)
	}
	unknown, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL(), "http")+"__brique/public/unknown", header)
	if unknown != nil {
		unknown.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("unpublished UI: status=%v err=%v", responseStatusCode(response), err)
	}
}

func TestWebHostAcceptsOnlyConfiguredHTTPSOriginBehindProxy(t *testing.T) {
	observedAddresses := make(chan string, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{"brique.public-web.v1"}, CheckOrigin: func(request *http.Request) bool {
		return request.Header.Get("Origin") == "http://"+request.Host
	}}
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedAddresses <- request.Header.Get(publicWebObservedAddressHeader)
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err == nil {
			_ = connection.Close()
		}
	}))
	defer backend.Close()
	backendWS := "ws" + strings.TrimPrefix(backend.URL, "http") + "/__brique/public/"
	server, err := Start(Config{Address: "127.0.0.1:0", PublicationDirectory: testPublication(t),
		PublicWebBackend: backendWS, AllowedHosts: []string{"public.example"},
		PublicOrigins: []string{"https://public.example"}, TrustedProxyAddresses: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	request, err := http.NewRequest(http.MethodGet, server.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "public.example"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Security-Policy"), "wss://public.example") {
		t.Fatalf("public HTTPS host response: status=%d csp=%q", response.StatusCode, response.Header.Get("Content-Security-Policy"))
	}

	dialer := websocket.Dialer{Subprotocols: []string{"brique.public-web.v1"},
		NetDial: func(network, _ string) (net.Conn, error) { return net.Dial(network, server.Address()) }}
	connection, response, err := dialer.Dial("ws://public.example/__brique/public/main",
		http.Header{"Origin": []string{"https://public.example"},
			publicWebClientAddressHeader: []string{"198.51.100.77"}})
	if err != nil {
		t.Fatalf("configured public origin: status=%v err=%v", responseStatusCode(response), err)
	}
	_ = connection.Close()
	if observed := <-observedAddresses; observed != "198.51.100.77" {
		t.Fatalf("trusted proxy observed address = %q", observed)
	}
	bad, response, err := dialer.Dial("ws://public.example/__brique/public/main",
		http.Header{"Origin": []string{"http://public.example"}})
	if bad != nil {
		_ = bad.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("insecure public origin: status=%v err=%v", responseStatusCode(response), err)
	}

	if rejected, err := Start(Config{PublicationDirectory: testPublication(t), PublicWebBackend: backendWS,
		AllowedHosts: []string{"public.example"}, PublicOrigins: []string{"https://other.example"}}); err == nil {
		_ = rejected.Close(context.Background())
		t.Fatal("origin whose authority is not allowed was accepted")
	}
	if rejected, err := Start(Config{PublicationDirectory: testPublication(t),
		TrustedProxyAddresses: []string{"not-an-ip"}}); err == nil {
		_ = rejected.Close(context.Background())
		t.Fatal("invalid trusted proxy address was accepted")
	}
}

func responseStatusCode(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}

func writeTestPublicationManifest(t testing.TB, directory string) {
	t.Helper()
	bootstrap, err := json.Marshal(webBootstrap{
		Format: bootstrapFormat, ContextPath: "/root", ChildContextPaths: []string{"/root/app"},
		UIName: "main", UIAddressPrefix: "@ui_main", RootContextID: "/root",
		RootViewName: "Root", RootViewPath: "/assets/app.abcdef12.js", AdapterName: "react",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "bootstrap.json"), append(bootstrap, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := json.Marshal(uiCatalog{Format: uiCatalogFormat, Entries: []uiCatalogEntry{{
		ContextPath: "/root", UIName: "main", URL: "/", Bootstrap: "/bootstrap.json",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, uiCatalogName), append(catalog, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	files := make([]publicationFile, 0, 4)
	for _, name := range []string{"index.html", "assets/app.abcdef12.js", "bootstrap.json", uiCatalogName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		files = append(files, publicationFile{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest := publicationManifest{Format: publicationFormat, Entrypoint: "index.html", Bootstrap: "bootstrap.json", Catalog: uiCatalogName, Files: files}
	manifest.PublicationID = publicationManifestID(manifest)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, publicationManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWebHostServesImmutableSnapshotWithStrictHeaders(t *testing.T) {
	directory := testPublication(t)
	server, err := Start(Config{PublicationDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	response, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "assets/app.abcdef12.js") {
		t.Fatalf("index = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("missing strict headers: %#v", response.Header)
	}

	assetURL := server.URL() + "assets/app.abcdef12.js"
	assetResponse, err := http.Get(assetURL)
	if err != nil {
		t.Fatal(err)
	}
	assetBody, _ := io.ReadAll(assetResponse.Body)
	_ = assetResponse.Body.Close()
	if assetResponse.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || assetResponse.Header.Get("ETag") == "" {
		t.Fatalf("unexpected asset caching: %#v", assetResponse.Header)
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.abcdef12.js"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterMutation, err := http.Get(assetURL)
	if err != nil {
		t.Fatal(err)
	}
	mutatedBody, _ := io.ReadAll(afterMutation.Body)
	_ = afterMutation.Body.Close()
	if string(mutatedBody) != string(assetBody) {
		t.Fatalf("served snapshot changed: before=%q after=%q", assetBody, mutatedBody)
	}

	request, _ := http.NewRequest(http.MethodGet, assetURL, nil)
	request.Header.Set("If-None-Match", assetResponse.Header.Get("ETag"))
	notModified, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = notModified.Body.Close()
	if notModified.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional response = %d", notModified.StatusCode)
	}
}

func TestWebHostServesValidatedSecondaryUIRoute(t *testing.T) {
	directory := testPublication(t)
	token := "ui1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	secondaryDir := filepath.Join(directory, "ui", token)
	if err := os.MkdirAll(secondaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryDir, "index.html"), []byte("<!doctype html><main>child</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap, _ := json.Marshal(webBootstrap{
		Format: bootstrapFormat, ContextPath: "/root/child", ChildContextPaths: []string{},
		UIName: "child", UIAddressPrefix: "@ui_child", RootContextID: "/root/child",
		RootViewName: "Child", RootViewPath: "/assets/app.abcdef12.js", AdapterName: "react",
	})
	if err := os.WriteFile(filepath.Join(secondaryDir, "bootstrap.json"), bootstrap, 0o600); err != nil {
		t.Fatal(err)
	}
	route := "/ui/" + token + "/"
	catalog, _ := json.Marshal(uiCatalog{Format: uiCatalogFormat, Entries: []uiCatalogEntry{
		{ContextPath: "/root", UIName: "main", URL: "/", Bootstrap: "/bootstrap.json"},
		{ContextPath: "/root/child", UIName: "child", URL: route, Bootstrap: route + "bootstrap.json"},
	}})
	if err := os.WriteFile(filepath.Join(directory, uiCatalogName), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteTestManifest(t, directory, publicationFormat, uiCatalogName)
	inspection, err := InspectPublication(directory)
	if err != nil || len(inspection.UIs) != 2 || inspection.UIs[1].URL != route {
		t.Fatalf("secondary inspection = %#v err=%v", inspection, err)
	}
	server, err := Start(Config{PublicationDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	response, err := http.Get(server.URL() + strings.TrimPrefix(route, "/"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "child") || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("secondary response = %d %q %#v", response.StatusCode, body, response.Header)
	}
}

func TestWebHostDoesNotPublishItsInternalInventory(t *testing.T) {
	server, err := Start(Config{PublicationDirectory: testPublication(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	for _, name := range []string{publicationManifestName, uiCatalogName} {
		response, err := http.Get(server.URL() + name)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s response = %d", name, response.StatusCode)
		}
	}
}

func TestWebHostStillAcceptsLegacyV1Publication(t *testing.T) {
	directory := testPublication(t)
	if err := os.Remove(filepath.Join(directory, uiCatalogName)); err != nil {
		t.Fatal(err)
	}
	rewriteTestManifest(t, directory, publicationFormatV1, "")
	if _, err := InspectPublication(directory); err != nil {
		t.Fatalf("legacy v1 publication rejected: %v", err)
	}
}

func TestWebHostRejectsHostMethodsTraversalAndNonLoopback(t *testing.T) {
	server, err := Start(Config{PublicationDirectory: testPublication(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	wrongHost, _ := http.NewRequest(http.MethodGet, server.URL(), nil)
	wrongHost.Host = "attacker.invalid"
	response, err := http.DefaultClient.Do(wrongHost)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("wrong host response = %d", response.StatusCode)
	}

	post, _ := http.NewRequest(http.MethodPost, server.URL(), strings.NewReader("x"))
	response, err = http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST response = %d", response.StatusCode)
	}

	if _, err := Start(Config{Address: "0.0.0.0:0", PublicationDirectory: testPublication(t)}); err == nil {
		t.Fatal("non-loopback host started before public admission exists")
	}
}

func TestWebHostRejectsSymlinksUnknownTypesAndMissingIndex(t *testing.T) {
	directory := testPublication(t)
	if err := os.Symlink(filepath.Join(directory, "index.html"), filepath.Join(directory, "alias.html")); err == nil {
		if _, err := Start(Config{PublicationDirectory: directory}); err == nil {
			t.Fatal("publication symlink accepted")
		}
	}
	unknown := t.TempDir()
	if err := os.WriteFile(filepath.Join(unknown, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknown, "secret.pem"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(Config{PublicationDirectory: unknown}); err == nil {
		t.Fatal("unknown asset type accepted")
	}
	if _, err := Start(Config{PublicationDirectory: t.TempDir()}); err == nil {
		t.Fatal("publication without index accepted")
	}
}

func TestWebHostRejectsPublicationTampering(t *testing.T) {
	directory := testPublication(t)
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.abcdef12.js"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(Config{PublicationDirectory: directory}); err == nil {
		t.Fatal("tampered publication accepted")
	}
}

func TestInspectPublicationReturnsCanonicalIdentity(t *testing.T) {
	directory := testPublication(t)
	inspection, err := InspectPublication(directory)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(directory)
	if inspection.Directory != canonical || !publicationIDPattern.MatchString(inspection.PublicationID) ||
		len(inspection.UIs) != 1 || inspection.UIs[0].ContextPath != "/root" || inspection.UIs[0].URL != "/" {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestWebHostRejectsInvalidBootstrap(t *testing.T) {
	for name, mutate := range map[string]func(*webBootstrap){
		"physical context": func(value *webBootstrap) {
			value.RootContextID, value.ContextPath = "/Users/nico/root", "/Users/nico/root"
		},
		"unknown root asset": func(value *webBootstrap) { value.RootViewPath = "/assets/missing.js" },
		"wrong UI prefix":    func(value *webBootstrap) { value.UIAddressPrefix = "@ui_other" },
	} {
		t.Run(name, func(t *testing.T) {
			directory := testPublication(t)
			var bootstrap webBootstrap
			raw, err := os.ReadFile(filepath.Join(directory, "bootstrap.json"))
			if err != nil || json.Unmarshal(raw, &bootstrap) != nil {
				t.Fatal("fixture bootstrap is unreadable")
			}
			mutate(&bootstrap)
			updated, _ := json.Marshal(bootstrap)
			if err := os.WriteFile(filepath.Join(directory, "bootstrap.json"), updated, 0o600); err != nil {
				t.Fatal(err)
			}
			writeTestPublicationManifestForExistingBootstrap(t, directory)
			if _, err := Start(Config{PublicationDirectory: directory}); err == nil {
				t.Fatal("invalid bootstrap accepted")
			}
		})
	}
}

func writeTestPublicationManifestForExistingBootstrap(t testing.TB, directory string) {
	t.Helper()
	files := make([]publicationFile, 0, 4)
	for _, name := range []string{"assets/app.abcdef12.js", "bootstrap.json", "index.html", uiCatalogName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		files = append(files, publicationFile{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	}
	manifest := publicationManifest{Format: publicationFormat, Entrypoint: "index.html", Bootstrap: "bootstrap.json", Catalog: uiCatalogName, Files: files}
	manifest.PublicationID = publicationManifestID(manifest)
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, publicationManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteTestManifest(t testing.TB, directory, format, catalogName string) {
	t.Helper()
	files := make([]publicationFile, 0)
	err := filepath.WalkDir(directory, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(directory, filePath)
		if err != nil || relative == publicationManifestName {
			return err
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		files = append(files, publicationFile{Path: filepath.ToSlash(relative), Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest := publicationManifest{Format: format, Entrypoint: "index.html", Bootstrap: "bootstrap.json", Catalog: catalogName, Files: files}
	manifest.PublicationID = publicationManifestID(manifest)
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, publicationManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkPublicationHandler(b *testing.B) {
	data := []byte("export const value = 1;")
	digest := `"sha256-test"`
	handler := publicationHandler(map[string]asset{
		"/index.html": {contentType: "text/html; charset=utf-8", data: []byte("ok"), etag: digest},
		"/app.js":     {contentType: "text/javascript; charset=utf-8", data: data, etag: digest},
	}, map[string]bool{"127.0.0.1:8080": true}, nil)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/app.js", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatal(response.Code)
		}
	}
}

func BenchmarkEmbeddedHostFootprint(b *testing.B) {
	for _, count := range []int{1, 8} {
		b.Run(fmt.Sprintf("instances-%d", count), func(b *testing.B) {
			directory := testPublication(b)
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			servers := make([]*Server, 0, count)
			for range count {
				server, err := Start(Config{PublicationDirectory: directory})
				if err != nil {
					b.Fatal(err)
				}
				servers = append(servers, server)
			}
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			live := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			if live < 0 {
				live = 0
			}
			b.ReportMetric(float64(live)/1024, "KiB-live")
			b.ReportMetric(float64(live)/float64(count)/1024, "KiB-live/host")
			for _, server := range servers {
				_ = server.Close(context.Background())
			}
			b.ReportMetric(0, "ns/op")
		})
	}
}

// A client that stops reading must not hold a static response forever.
func TestStaticResponseStopsWhenTheClientStopsReading(t *testing.T) {
	previous := staticWriteIdleTimeout
	staticWriteIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { staticWriteIdleTimeout = previous })

	data := make([]byte, 96<<20)
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(len(data)))
		writeWithIdleDeadline(writer, data)
		close(finished)
	}))
	defer server.Close()

	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4096)
	}
	if _, err := connection.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the server kept writing to a client that never reads")
	}
}

func TestStaticResponseStillServesAClientThatKeepsReading(t *testing.T) {
	previous := staticWriteIdleTimeout
	staticWriteIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { staticWriteIdleTimeout = previous })
	data := bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 MiB, several chunks
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(len(data)))
		writeWithIdleDeadline(writer, data)
	}))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("a reading client got %d of %d bytes: %v", len(got), len(data), err)
	}
}
