// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- a real MCP server, in process ----------------------------------------------------------------

type textIn struct {
	Text string `json:"text" jsonschema:"the text"`
}
type sizeIn struct {
	Bytes int `json:"bytes"`
}
type none struct{}

type fakeServer struct {
	*httptest.Server
	mcp     *mcp.Server
	calls   atomic.Int64
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

// releaseAll lets every waiting "slow" call end; called by the tests and, last, by the cleanup, so that no handler is
// still waiting when the server closes.
func (f *fakeServer) releaseAll() { f.once.Do(func() { close(f.release) }) }

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{mcp: mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0"}, nil),
		release: make(chan struct{}), started: make(chan struct{}, 16)}
	mcp.AddTool(f.mcp, &mcp.Tool{Name: "echo", Description: "Echo a text."}, func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, none, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, none{}, nil
	})
	mcp.AddTool(f.mcp, &mcp.Tool{Name: "fail", Description: "Always a tool error."}, func(_ context.Context, _ *mcp.CallToolRequest, _ textIn) (*mcp.CallToolResult, none, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such order"}}}, none{}, nil
	})
	mcp.AddTool(f.mcp, &mcp.Tool{Name: "big", Description: "A large text."}, func(_ context.Context, _ *mcp.CallToolRequest, in sizeIn) (*mcp.CallToolResult, none, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("é", in.Bytes/2)}}}, none{}, nil
	})
	mcp.AddTool(f.mcp, &mcp.Tool{Name: "slow", Description: "Waits to be released."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ textIn) (*mcp.CallToolResult, none, error) {
		f.calls.Add(1)
		f.started <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "released"}}}, none{}, nil
	})
	mcp.AddTool(f.mcp, &mcp.Tool{Name: "image", Description: "An image."}, func(_ context.Context, _ *mcp.CallToolRequest, _ textIn) (*mcp.CallToolResult, none, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte("png")}}}, none{}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.mcp }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		// "drop": the server reads the whole request, then the connection dies with no answer.
		if bytes.Contains(body, []byte(`"tools/call"`)) && bytes.Contains(body, []byte(`"drop"`)) {
			f.calls.Add(1)
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Server.Close)
	t.Cleanup(f.releaseAll) // runs before the Close above (cleanups run last registered first)
	return f
}

// ---- registry, keys, gateway ------------------------------------------------------------------------

type fixture struct {
	t       *testing.T
	dir     string
	key     ed25519.PrivateKey
	servers map[string]map[string]any
	plans   []string
	version int64
	gw      *Gateway
	now     time.Time
}

func newFixture(t *testing.T, endpoint string) *fixture {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	f := &fixture{t: t, dir: t.TempDir(), key: key, now: time.Now(), servers: map[string]map[string]any{
		"local": {"transport": "streamable_http", "endpoint": endpoint, "allow_private_network": true,
			"auth": map[string]any{"mode": "none"}, "catalog": map[string]any{"mode": "all"},
			"tools": map[string]any{"echo": map[string]any{"arguments": map[string]any{"deny": []string{"password"}}}}},
	}, plans: []string{"/root/shop/capacity/order.sync"}}
	f.writeRegistry()
	return f
}

func (f *fixture) writeRegistry() {
	f.t.Helper()
	f.version++
	reg := map[string]any{"format": "brique-mcp-registry", "version": f.version,
		"authority_public_key": base64.RawURLEncoding.EncodeToString(f.key.Public().(ed25519.PublicKey)),
		"plans":                f.plans, "servers": f.servers}
	raw, _ := json.Marshal(reg)
	tmp := filepath.Join(f.dir, "registry.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, RegistryFile)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) open(secrets map[string]string) *Gateway {
	f.t.Helper()
	gw, err := New(Config{StoreDir: f.dir, Secrets: secrets, Now: func() time.Time { return f.now }})
	if err != nil {
		f.t.Fatal(err)
	}
	f.gw = gw
	f.t.Cleanup(gw.Close)
	return gw
}

func (f *fixture) project(servers ...string) map[string]any {
	f.t.Helper()
	out, err := f.gw.Tools(context.Background(), ToolsRequest{Servers: servers})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) envelope(callID, projectionID, tool, args string) string {
	return signEnvelope(f.key, Claims{CallID: callID, ProjectionID: projectionID, Tool: tool, ArgsSHA256: sha256Hex([]byte(args)),
		Pilot: "/root/p", Continuity: "c1", Exp: f.now.Add(5 * time.Minute).Unix(), Nonce: base64.RawURLEncoding.EncodeToString([]byte(callID + time.Now().String()))})
}

func (f *fixture) agentic(callID, projectionID, tool, args string) map[string]any {
	f.t.Helper()
	out, err := f.gw.Call(context.Background(), CallRequest{CallID: callID, Origin: OriginAgentic, Envelope: f.envelope(callID, projectionID, tool, args),
		ProjectionID: projectionID, Tool: tool, ArgumentsJSON: args})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func state(out map[string]any) string  { s, _ := out["state"].(string); return s }
func reason(out map[string]any) string { s, _ := out["reason"].(string); return s }

func resultText(t *testing.T, out map[string]any) string {
	t.Helper()
	raw, _ := out["result"].(json.RawMessage)
	var r struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("no result text in %v", out)
	}
	return r.Content[0].Text
}

// ---- tools ------------------------------------------------------------------------------------------

func TestToolsProjectsWithStableNamesAndPersists(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	out := f.project("local", "unknown")
	tools := out["tools"].([]map[string]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool["model_name"].(string)] = true
	}
	for _, want := range []string{"local__echo", "local__fail", "local__big", "local__slow", "local__image"} {
		if !names[want] {
			t.Errorf("projection lacks %s: %v", want, names)
		}
	}
	unavailable := out["unavailable"].([]map[string]any)
	if len(unavailable) != 1 || unavailable[0]["server"] != "unknown" || unavailable[0]["reason"] != reasonNotRegistered {
		t.Fatalf("unavailable = %v", unavailable)
	}
	id := out["projection_id"].(string)
	if _, err := os.Stat(filepath.Join(f.dir, projectionsDir, id+".json")); err != nil {
		t.Fatalf("the projection is not persisted: %v", err)
	}
	if _, err := f.gw.Tools(context.Background(), ToolsRequest{Servers: []string{"local"}, Mandatory: []string{"other"}}); err == nil {
		t.Fatal("a mandatory server that is not requested was accepted")
	}
	if _, err := f.gw.Tools(context.Background(), ToolsRequest{Servers: []string{"unknown"}, Mandatory: []string{"unknown"}}); err == nil {
		t.Fatal("an unavailable mandatory server did not refuse the projection")
	}
}

func TestModelNamesAreSafeUniqueAndBounded(t *testing.T) {
	names := modelNames([][2]string{{"crm", "customer.read"}, {"crm", "customer_read"}, {"a", strings.Repeat("x", 100)}, {"crm", "plain"}})
	seen := map[string]bool{}
	for _, name := range names {
		if !regexpModelName(name) || seen[name] {
			t.Fatalf("bad or duplicate model name %q in %v", name, names)
		}
		seen[name] = true
	}
	if names[3] != "crm__plain" {
		t.Fatalf("an already safe name was changed: %q", names[3])
	}
	if names[0] == "crm__customer_read" {
		t.Fatal("a sanitized name collides with a genuine one")
	}
}

func regexpModelName(name string) bool {
	return len(name) >= 1 && len(name) <= maxModelNameLength && !modelNameUnsafe.MatchString(name)
}

// ---- agentic calls --------------------------------------------------------------------------------

func TestAgenticCallIsSentOnceAndReplaysTheSameAttempt(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	args := `{"text":"bonjour"}`
	envelope := f.envelope("c1", projection, "local__echo", args)
	req := CallRequest{CallID: "c1", Origin: OriginAgentic, Envelope: envelope, ProjectionID: projection, Tool: "local__echo", ArgumentsJSON: args}
	first, err := f.gw.Call(context.Background(), req)
	if err != nil || state(first) != stateCompleted || resultText(t, first) != "bonjour" || first["transmission"] != transmissionComplete {
		t.Fatalf("first call: %v %v", first, err)
	}
	again, err := f.gw.Call(context.Background(), req)
	if err != nil || again["attempt_id"] != first["attempt_id"] || state(again) != stateCompleted {
		t.Fatalf("replay: %v %v", again, err)
	}
	if server.calls.Load() != 1 {
		t.Fatalf("the server was called %d times", server.calls.Load())
	}
	other := f.agentic("c1", projection, "local__echo", args) // same call_id, another envelope (another nonce)
	if state(other) != stateRefused || reason(other) != reasonPolicyDenied || server.calls.Load() != 1 {
		t.Fatalf("a different envelope for an existing call_id: %v", other)
	}
	status, _ := f.gw.Status("c1")
	if status["known"] != true || status["state"] != stateCompleted || status["attempt_id"] != first["attempt_id"] {
		t.Fatalf("status = %v", status)
	}
	if unknown, _ := f.gw.Status("never"); unknown["known"] != false {
		t.Fatalf("status of an unknown call_id = %v", unknown)
	}
}

func TestAnEnvelopeThatDoesNotBindTheCallIsRefused(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	args := `{"text":"x"}`
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]CallRequest{
		"forged": {CallID: "k1", Envelope: signEnvelope(otherKey, Claims{CallID: "k1", ProjectionID: projection, Tool: "local__echo",
			ArgsSHA256: sha256Hex([]byte(args)), Exp: f.now.Add(time.Minute).Unix(), Nonce: "n"}), ProjectionID: projection, Tool: "local__echo", ArgumentsJSON: args},
		"expired": {CallID: "k2", Envelope: signEnvelope(f.key, Claims{CallID: "k2", ProjectionID: projection, Tool: "local__echo",
			ArgsSHA256: sha256Hex([]byte(args)), Exp: f.now.Add(-time.Second).Unix(), Nonce: "n"}), ProjectionID: projection, Tool: "local__echo", ArgumentsJSON: args},
		"arguments changed": {CallID: "k3", Envelope: f.envelope("k3", projection, "local__echo", args), ProjectionID: projection, Tool: "local__echo", ArgumentsJSON: `{"text":"y"}`},
		"other tool":        {CallID: "k4", Envelope: f.envelope("k4", projection, "local__echo", args), ProjectionID: projection, Tool: "local__fail", ArgumentsJSON: args},
		"other call_id":     {CallID: "k5", Envelope: f.envelope("k6", projection, "local__echo", args), ProjectionID: projection, Tool: "local__echo", ArgumentsJSON: args},
		"unknown tool":      {CallID: "k7", Envelope: f.envelope("k7", projection, "local__nope", args), ProjectionID: projection, Tool: "local__nope", ArgumentsJSON: args},
	}
	for name, req := range cases {
		req.Origin = OriginAgentic
		out, err := f.gw.Call(context.Background(), req)
		if err != nil || state(out) != stateRefused {
			t.Errorf("%s: %v %v", name, out, err)
		}
	}
	if _, err := f.gw.Call(context.Background(), CallRequest{CallID: "k8", Origin: OriginAgentic, Tool: "local__echo"}); err == nil {
		t.Error("a call without envelope was not refused as invalid")
	}
	if _, err := f.gw.Call(context.Background(), CallRequest{CallID: "bad id", Origin: OriginAgentic}); err == nil {
		t.Error("a malformed call_id was accepted")
	}
	if server.calls.Load() != 0 {
		t.Fatalf("a refused call reached the server %d times", server.calls.Load())
	}
}

func TestArgumentsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(map[string]string{"crm_token": "s3cr3t-value"})
	projection := f.project("local")["projection_id"].(string)
	for args, want := range map[string]string{
		`{"text":12}`:                         reasonInvalidArguments,
		`{"text":"x","password":"p"}`:         reasonDisclosureDenied,
		`{"text":"leak s3cr3t-value please"}`: reasonDisclosureDenied,
		`[1,2]`:                               reasonInvalidArguments,
	} {
		out := f.agentic("a"+sha256Hex([]byte(args))[:8], projection, "local__echo", args)
		if state(out) != stateRefused || reason(out) != want {
			t.Errorf("%s: %v, want %s", args, out, want)
		}
	}
	if server.calls.Load() != 0 {
		t.Fatal("a refused argument reached the server")
	}
}

func TestARemoteToolErrorIsADefiniteResult(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	out := f.agentic("e1", projection, "local__fail", `{"text":"x"}`)
	if state(out) != stateRemoteError || out["transmission"] != transmissionComplete || resultText(t, out) != "no such order" {
		t.Fatalf("tool error: %v", out)
	}
}

// ---- plans ----------------------------------------------------------------------------------------

func TestOnlyARegisteredPlanMayCall(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	ok, err := f.gw.Call(context.Background(), CallRequest{CallID: "p1", Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": "from a plan"}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
	if err != nil || state(ok) != stateCompleted || resultText(t, ok) != "from a plan" {
		t.Fatalf("registered plan: %v %v", ok, err)
	}
	refused, _ := f.gw.Call(context.Background(), CallRequest{CallID: "p2", Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": "x"}, CallerContext: "/root/agentic", CallerCapacity: "pilot.activate"})
	if state(refused) != stateRefused || reason(refused) != reasonPolicyDenied {
		t.Fatalf("unregistered caller: %v", refused)
	}
	if server.calls.Load() != 1 {
		t.Fatalf("server calls = %d", server.calls.Load())
	}
}

// ---- registry changes --------------------------------------------------------------------------------

func TestARevocationTakesEffectOnTheNextCall(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	f.servers["local"]["catalog"] = map[string]any{"mode": "explicit", "tools": []string{"fail"}}
	f.writeRegistry()
	if out := f.agentic("r1", projection, "local__echo", `{"text":"x"}`); state(out) != stateRefused || reason(out) != reasonPolicyDenied {
		t.Fatalf("a tool removed from the registry: %v", out)
	}
	os.Remove(filepath.Join(f.dir, RegistryFile))
	if out := f.agentic("r2", projection, "local__fail", `{"text":"x"}`); state(out) != stateRefused {
		t.Fatalf("no registry: %v", out)
	}
	if _, err := f.gw.Tools(context.Background(), ToolsRequest{}); err == nil {
		t.Fatal("tools without a registry was not refused")
	}
	os.WriteFile(filepath.Join(f.dir, RegistryFile), []byte(`{"format":"brique-mcp-registry","version":1,"servers":{},"typo":1}`), 0o600)
	if _, err := f.gw.Tools(context.Background(), ToolsRequest{}); err == nil {
		t.Fatal("a registry with an unknown field was accepted")
	}
	if server.calls.Load() != 0 {
		t.Fatal("a revoked call reached the server")
	}
}

func TestAChangedToolMakesTheProjectionStale(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	mcp.AddTool(server.mcp, &mcp.Tool{Name: "echo", Description: "Now something else."}, func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, none, error) {
		server.calls.Add(1)
		return &mcp.CallToolResult{}, none{}, nil
	})
	f.now = f.now.Add(catalogTTL + time.Second) // the cached listing is old: the call lists again
	if out := f.agentic("s1", projection, "local__echo", `{"text":"x"}`); state(out) != stateRefused || reason(out) != reasonProjectionStale {
		t.Fatalf("changed tool: %v", out)
	}
	if server.calls.Load() != 0 {
		t.Fatal("a stale call reached the server")
	}
}

// ---- load, cancellation, failures ---------------------------------------------------------------------

func TestASaturatedServerRefusesAtOnce(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.servers["local"]["limits"] = map[string]any{"concurrency": 1}
	f.writeRegistry()
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	done := make(chan map[string]any)
	go func() { done <- f.agentic("o1", projection, "local__slow", `{"text":"x"}`) }()
	<-server.started
	start := time.Now()
	if out := f.agentic("o2", projection, "local__echo", `{"text":"x"}`); state(out) != stateRefused || reason(out) != reasonOverloaded {
		t.Fatalf("second call on a saturated server: %v", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the refusal waited")
	}
	server.releaseAll()
	if out := <-done; state(out) != stateCompleted {
		t.Fatalf("first call: %v", out)
	}
}

func TestCancelEndsACallInFlightAsOutcomeUnknown(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	gw := f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	done := make(chan map[string]any)
	go func() { done <- f.agentic("x1", projection, "local__slow", `{"text":"x"}`) }()
	<-server.started
	gw.Cancel()
	out := <-done
	if state(out) != stateOutcomeUnknown || out["transmission"] != transmissionComplete {
		t.Fatalf("cancelled call: %v", out)
	}
	if a, _ := gw.journal.get("x1"); a.State != stateOutcomeUnknown {
		t.Fatalf("journal: %v", a)
	}
}

func TestATransportFailureIsClassifiedByWhatWasWritten(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	// "drop": the request was written entirely, the answer never came.
	f.servers["local"]["catalog"] = map[string]any{"mode": "all"}
	plan := func(id, text string) map[string]any {
		out, err := f.gw.Call(context.Background(), CallRequest{CallID: id, Origin: OriginPlan, Server: "local", Tool: "echo",
			Arguments: map[string]any{"text": text}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := plan("t1", "drop"); state(out) != stateOutcomeUnknown || out["transmission"] != transmissionComplete {
		t.Fatalf("dropped after the request: %v", out)
	}
	// The server is gone: nothing can be written.
	if out := plan("t2", "warm"); state(out) != stateCompleted {
		t.Fatalf("warm-up: %v", out)
	}
	server.Close()
	out := plan("t3", "x")
	if state(out) != stateFailedBeforeTransmit || out["transmission"] != transmissionNone {
		t.Fatalf("server gone: %v", out)
	}
}

func TestAPrivateAddressIsNeverDialedUnlessAllowed(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, strings.Replace(server.URL, "http://", "https://", 1))
	f.servers["local"]["allow_private_network"] = false
	f.writeRegistry()
	f.open(nil)
	out := f.project("local")
	unavailable := out["unavailable"].([]map[string]any)
	if len(unavailable) != 1 || unavailable[0]["reason"] != reasonTransport {
		t.Fatalf("a loopback server was reached: %v", out)
	}
	if !blockedIP([]byte{169, 254, 169, 254}) || !blockedIP([]byte{10, 0, 0, 1}) || !blockedIP([]byte{100, 64, 0, 1}) || blockedIP([]byte{93, 184, 216, 34}) {
		t.Fatal("blockedIP")
	}
}

func TestResultsAreBoundedAndNonTextIsDescribed(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.servers["local"]["limits"] = map[string]any{"result_inline_bytes": 4096}
	f.writeRegistry()
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	big := f.agentic("b1", projection, "local__big", `{"bytes":100000}`)
	raw := big["result"].(json.RawMessage)
	if state(big) != stateCompleted || len(raw) > 4096 || !bytes.Contains(raw, []byte(`"truncated":true`)) {
		t.Fatalf("big result: %d bytes, %s", len(raw), raw[:min(len(raw), 200)])
	}
	image := f.agentic("b2", projection, "local__image", `{"text":"x"}`)
	if text := resultText(t, image); !strings.Contains(text, "image content not included") {
		t.Fatalf("image: %q", text)
	}
}

// ---- journal ----------------------------------------------------------------------------------------

func TestARestartReconcilesWhatWasInFlight(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, journalDir), 0o700)
	var lines bytes.Buffer
	for _, a := range []Attempt{
		{CallID: "j1", AttemptID: "a1", State: stateReserved, Transmission: transmissionNone},
		{CallID: "j2", AttemptID: "a2", State: stateReserved, Transmission: transmissionNone},
		{CallID: "j2", AttemptID: "a2", State: stateTransmitting, Transmission: transmissionPossible},
		{CallID: "j3", AttemptID: "a3", State: stateCompleted, Transmission: transmissionComplete, At: time.Now()},
	} {
		line, _ := json.Marshal(a)
		lines.Write(append(line, '\n'))
	}
	lines.WriteString(`{"call_id":"j4","state":"transm`) // torn last line: never committed
	os.WriteFile(filepath.Join(dir, journalDir, journalFile), lines.Bytes(), 0o600)
	gw, err := New(Config{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	for id, want := range map[string]string{"j1": stateFailedBeforeTransmit, "j2": stateOutcomeUnknown, "j3": stateCompleted} {
		if s, _ := gw.Status(id); s["state"] != want {
			t.Errorf("%s: %v, want %s", id, s, want)
		}
	}
	if s, _ := gw.Status("j4"); s["known"] != false {
		t.Errorf("a torn line was read: %v", s)
	}
	if s, _ := gw.Status("j2"); s["transmission"] != transmissionPossible {
		t.Errorf("j2 transmission: %v", s)
	}
}

func TestConcurrentDuplicatesShareOneAttempt(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	projection := f.project("local")["projection_id"].(string)
	args := `{"text":"x"}`
	req := CallRequest{CallID: "d1", Origin: OriginAgentic, Envelope: f.envelope("d1", projection, "local__slow", args), ProjectionID: projection, Tool: "local__slow", ArgumentsJSON: args}
	var wg sync.WaitGroup
	outs := make([]map[string]any, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); outs[i], _ = f.gw.Call(context.Background(), req) }(i)
	}
	<-server.started
	time.Sleep(100 * time.Millisecond)
	server.releaseAll()
	wg.Wait()
	for _, out := range outs {
		if state(out) != stateCompleted || out["attempt_id"] != outs[0]["attempt_id"] {
			t.Fatalf("duplicates: %v", outs)
		}
	}
	if server.calls.Load() != 1 {
		t.Fatalf("the server was called %d times", server.calls.Load())
	}
}

// ---- egress -----------------------------------------------------------------------------------------

func TestCredentialsGoOnlyToTheServersHost(t *testing.T) {
	var seen atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.invalid/", http.StatusTemporaryRedirect)
		}
	}))
	defer target.Close()
	reg, err := parseRegistry([]byte(`{"format":"brique-mcp-registry","version":1,"servers":{"s":{"transport":"streamable_http","endpoint":"` + target.URL +
		`/mcp","allow_private_network":true,"auth":{"mode":"static","secrets":{"token":"tok"}},"catalog":{"mode":"all"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	client := newServerClient(reg.Servers["s"], map[string]string{"tok": "abc"})
	resp, err := client.Get(target.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen.Load() != "Bearer abc" {
		t.Fatalf("authorization = %v", seen.Load())
	}
	if _, err := client.Get("http://other.invalid/mcp"); err == nil {
		t.Fatal("a request to another host was sent")
	}
	if _, err := client.Get(target.URL + "/redirect"); err == nil {
		t.Fatal("a redirect was followed")
	}
}

func TestRegistryValidation(t *testing.T) {
	base := `{"format":"brique-mcp-registry","version":1,"servers":{"s":%s}}`
	good := `{"transport":"streamable_http","endpoint":"https://mcp.example.com/mcp","auth":{"mode":"none"},"catalog":{"mode":"all"}}`
	if _, err := parseRegistry([]byte(strings.Replace(base, "%s", good, 1))); err != nil {
		t.Fatalf("a valid registry was refused: %v", err)
	}
	for name, server := range map[string]string{
		"http without private":   strings.Replace(good, "https://", "http://", 1),
		"user in url":            strings.Replace(good, "https://", "https://u:p@", 1),
		"stdio":                  strings.Replace(good, "streamable_http", "stdio", 1),
		"oauth not yet":          strings.Replace(good, `"none"`, `"oauth"`, 1),
		"static without token":   strings.Replace(good, `{"mode":"none"}`, `{"mode":"static"}`, 1),
		"explicit without tools": strings.Replace(good, `{"mode":"all"}`, `{"mode":"explicit"}`, 1),
		"deadline too long":      strings.Replace(good, `"catalog"`, `"limits":{"call_deadline_s":1000},"catalog"`, 1),
		"unknown field":          strings.Replace(good, `"catalog"`, `"catalogue":1,"catalog"`, 1),
	} {
		if _, err := parseRegistry([]byte(strings.Replace(base, "%s", server, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAJSONRPCErrorOfTheServerIsADefiniteRemoteError(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	f.project("local") // the listing is cached: the call does not list again
	server.mcp.RemoveTools("echo")
	out, err := f.gw.Call(context.Background(), CallRequest{CallID: "j1", Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": "x"}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
	if err != nil || state(out) != stateRemoteError || reason(out) != reasonProtocol || out["transmission"] != transmissionComplete {
		t.Fatalf("server error: %v %v", out, err)
	}
}
