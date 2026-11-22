package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fakes -----------------------------------------------------------------------------------------------------------------

// longOutbound is a fake Brique outbound whose capacity acknowledges itself `running` twice before answering.
type longOutbound struct{ plainCalls int }

func (o *longOutbound) Initialize() (map[string]any, error) { return nil, nil }
func (o *longOutbound) Framework() (string, error) { return "", nil }
func (o *longOutbound) FrameworkPath() (string, error)      { return "", nil }
func (o *longOutbound) ForwardIntention(map[string]any) (map[string]any, error) {
	o.plainCalls++
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"final":true}`}}}, nil
}
func (o *longOutbound) ForwardIntentionWithProgress(_ map[string]any, onRunning func(int, time.Duration)) (map[string]any, error) {
	onRunning(1, 10*time.Second)
	onRunning(2, 20*time.Second)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"final":true}`}}}, nil
}

// blockingOutbound holds every call until released: it keeps requests in flight.
type blockingOutbound struct{ release chan struct{} }

func (o *blockingOutbound) Initialize() (map[string]any, error) { return nil, nil }
func (o *blockingOutbound) Framework() (string, error) { return "", nil }
func (o *blockingOutbound) FrameworkPath() (string, error)      { return "", nil }
func (o *blockingOutbound) ForwardIntention(map[string]any) (map[string]any, error) {
	<-o.release
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "late"}}}, nil
}

type syncRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (s *syncRecorder) Header() http.Header { return s.rec.Header() }
func (s *syncRecorder) WriteHeader(c int)   { s.rec.WriteHeader(c) }
func (s *syncRecorder) Flush()              {}
func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(b)
}
func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Body.String()
}

func dispatchToolCall(t *testing.T, params string) (frames []string, outbound *longOutbound) {
	t.Helper()
	outbound = &longOutbound{}
	h := NewMCPHandler(outbound)
	sess, err := h.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	rec := &syncRecorder{rec: httptest.NewRecorder()}
	done := make(chan struct{})
	served := make(chan struct{})
	go func() { h.Serve(sess, rec, rec, done); close(served) }()
	h.Dispatch(sess, JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "tools/call", Params: json.RawMessage(params)})
	time.Sleep(20 * time.Millisecond)
	close(done)
	<-served
	for _, line := range strings.Split(rec.body(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	return frames, outbound
}

// --- a real server and real SSE clients -----------------------------------------------------------------------------------------

type testServer struct {
	*httptest.Server
	mcp *MCPServer
}

func startServer(t *testing.T, outbound BriqueOutbound, origins ...string) *testServer {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	mcp := NewMCPServer(ts.Listener.Addr().String(), NewMCPHandler(outbound))
	mcp.guard = newGuard(ts.Listener.Addr().String(), origins)
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", mcp.handleSSE)
	mux.HandleFunc("/message", mcp.handleMessage)
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return &testServer{Server: ts, mcp: mcp}
}

// client is one SSE client: it follows the endpoint the server announced and collects the frames of its own stream.
type client struct {
	endpoint string
	frames   chan string
	body     io.Closer
}

func (ts *testServer) connect(t *testing.T) *client {
	t.Helper()
	resp, err := http.Get(ts.URL + "/sse")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("sse: %v %v", err, resp)
	}
	c := &client{frames: make(chan string, 64), body: resp.Body}
	t.Cleanup(func() { resp.Body.Close() })
	endpoint := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data := strings.TrimPrefix(line, "data: ")
				if event == "endpoint" {
					endpoint <- data
				} else {
					c.frames <- data
				}
			}
		}
	}()
	select {
	case c.endpoint = <-endpoint:
	case <-time.After(2 * time.Second):
		t.Fatal("the server did not announce its endpoint")
	}
	return c
}

func (ts *testServer) post(t *testing.T, endpoint, body string, mutate ...func(*http.Request)) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, m := range mutate {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (c *client) next(t *testing.T) string {
	t.Helper()
	select {
	case f := <-c.frames:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame on the stream")
		return ""
	}
}

func (c *client) silent(t *testing.T) {
	t.Helper()
	select {
	case f := <-c.frames:
		t.Fatalf("a frame reached a client it was not for: %s", f)
	case <-time.After(150 * time.Millisecond):
	}
}

// --- tests ---------------------------------------------------------------------------------------------------------------------

func TestHandleMessageRejectsBulkPayload(t *testing.T) {
	h := NewMCPHandler(nil)
	server := NewMCPServer("", h)
	sess, _ := h.OpenSession()
	body := `{"jsonrpc":"2.0","method":"x","params":"` + strings.Repeat("x", int(maxControlMessageBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/message?session="+sess.id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleMessage(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRunningAcknowledgementsBecomeProgressNotifications(t *testing.T) {
	frames, _ := dispatchToolCall(t, `{"name":"brique","arguments":{"to":{"context":"/root","cap":"x","type":"user"}},"_meta":{"progressToken":"tok-1"}}`)
	if len(frames) != 3 {
		t.Fatalf("frames=%d want 2 notifications then the result: %v", len(frames), frames)
	}
	for i := 0; i < 2; i++ {
		var n struct {
			Method string `json:"method"`
			Params struct {
				ProgressToken string  `json:"progressToken"`
				Progress      float64 `json:"progress"`
				Message       string  `json:"message"`
			} `json:"params"`
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(frames[i]), &n); err != nil {
			t.Fatal(err)
		}
		if n.Method != "notifications/progress" || n.Params.ProgressToken != "tok-1" || int(n.Params.Progress) != i+1 || len(n.ID) != 0 {
			t.Fatalf("frame %d is not the expected progress notification: %s", i, frames[i])
		}
	}
	if !strings.Contains(frames[2], `"id":7`) || !strings.Contains(frames[2], `final`) {
		t.Fatalf("the last frame must be the call's single result: %s", frames[2])
	}
}

func TestWithoutProgressTokenNothingIsNotified(t *testing.T) {
	frames, outbound := dispatchToolCall(t, `{"name":"brique","arguments":{"to":{"context":"/root","cap":"x","type":"user"}}}`)
	if len(frames) != 1 || outbound.plainCalls != 1 {
		t.Fatalf("a request without progressToken gets only its result: frames=%v plain=%d", frames, outbound.plainCalls)
	}
}

func TestListenAddressIsTheLoopbackUnlessOpenedOnPurpose(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:18200", "localhost:18200", "[::1]:18200", "127.0.0.2:1"} {
		if err := checkListenAddr(ok, false); err != nil {
			t.Errorf("%s must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{":18200", "0.0.0.0:18200", "[::]:18200", "192.168.1.5:18200", "example.com:18200"} {
		if err := checkListenAddr(bad, false); err == nil {
			t.Errorf("%s must be refused without BRIQUE_MCP_ALLOW_REMOTE", bad)
		}
		if err := checkListenAddr(bad, true); err != nil {
			t.Errorf("%s must be accepted when opened on purpose: %v", bad, err)
		}
	}
	if err := checkListenAddr("no-port", true); err == nil {
		t.Error("an address that is not host:port is refused whatever the operator says")
	}
}

func TestWhatAWebPageCanSendToLocalhostIsRefused(t *testing.T) {
	ts := startServer(t, &longOutbound{})
	c := ts.connect(t)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"the client of the protocol", func(*http.Request) {}, http.StatusAccepted},
		{"a browser page: it sends its Origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"DNS rebinding: the Host is a name that points here", func(r *http.Request) { r.Host = "rebound.example:" + strings.Split(r.URL.Host, ":")[1] }, http.StatusForbidden},
		{"a simple request: text/plain needs no preflight", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"no content type at all", func(r *http.Request) { r.Header.Del("Content-Type") }, http.StatusUnsupportedMediaType},
		{"json with a charset is json", func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }, http.StatusAccepted},
	}
	for _, tc := range cases {
		if got := ts.post(t, c.endpoint, ping, tc.mutate).StatusCode; got != tc.want {
			t.Errorf("%s: status %d want %d", tc.name, got, tc.want)
		}
	}
	// the stream is guarded as well
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/sse", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a page must not open the stream: %v %v", err, resp)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestAnOriginIsAcceptedOnlyWhenTheOperatorNamedIt(t *testing.T) {
	ts := startServer(t, &longOutbound{}, "http://my-tool.local")
	c := ts.connect(t)
	named := ts.post(t, c.endpoint, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, func(r *http.Request) { r.Header.Set("Origin", "http://my-tool.local") })
	other := ts.post(t, c.endpoint, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, func(r *http.Request) { r.Header.Set("Origin", "http://other.local") })
	if named.StatusCode != http.StatusAccepted || other.StatusCode != http.StatusForbidden {
		t.Fatalf("named=%d other=%d", named.StatusCode, other.StatusCode)
	}
}

func TestEachClientHearsItsOwnAnswersOnly(t *testing.T) {
	ts := startServer(t, &longOutbound{})
	a, b := ts.connect(t), ts.connect(t)
	if a.endpoint == b.endpoint {
		t.Fatal("two clients must not share a session")
	}
	// the same JSON-RPC id in both sessions: each gets exactly one answer, on its own stream
	ts.post(t, a.endpoint, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	ts.post(t, b.endpoint, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	for _, c := range []*client{a, b} {
		if f := c.next(t); !strings.Contains(f, `"id":1`) {
			t.Fatalf("unexpected frame %s", f)
		}
		c.silent(t)
	}
	// an answer for A never reaches B
	ts.post(t, a.endpoint, `{"jsonrpc":"2.0","id":99,"method":"ping"}`)
	if f := a.next(t); !strings.Contains(f, `"id":99`) {
		t.Fatalf("A must hear its own answer: %s", f)
	}
	b.silent(t)
}

func TestProgressGoesToTheCallerOnly(t *testing.T) {
	ts := startServer(t, &longOutbound{})
	a, b := ts.connect(t), ts.connect(t)
	ts.post(t, a.endpoint, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"brique","arguments":{"to":{"context":"/root","cap":"x","type":"user"}},"_meta":{"progressToken":"p1"}}}`)
	for i := 0; i < 3; i++ { // two progress notifications, then the result
		a.next(t)
	}
	b.silent(t)
}

func TestAPostWithoutAKnownSessionIsRefused(t *testing.T) {
	ts := startServer(t, &longOutbound{})
	for _, endpoint := range []string{"/message", "/message?session=", "/message?session=guessed", "/message?session=" + strings.Repeat("0", 32)} {
		if got := ts.post(t, endpoint, `{"jsonrpc":"2.0","id":1,"method":"ping"}`).StatusCode; got != http.StatusNotFound {
			t.Errorf("%s: status %d want 404", endpoint, got)
		}
	}
}

func TestASessionEndsWithItsStream(t *testing.T) {
	ts := startServer(t, &longOutbound{})
	c := ts.connect(t)
	c.body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for ts.mcp.handler.Session(strings.TrimPrefix(c.endpoint, "/message?session=")) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the session of a closed stream must be forgotten")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := ts.post(t, c.endpoint, `{"jsonrpc":"2.0","id":1,"method":"ping"}`).StatusCode; got != http.StatusNotFound {
		t.Fatalf("a closed session answers 404, got %d", got)
	}
}

func TestTooManyRequestsInFlightAreRefusedNotQueued(t *testing.T) {
	block := &blockingOutbound{release: make(chan struct{})}
	ts := startServer(t, block)
	c := ts.connect(t)
	call := func(id int) int {
		return ts.post(t, c.endpoint, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"brique","arguments":{"to":{"context":"/root","cap":"x","type":"user"}}}}`, id)).StatusCode
	}
	for i := 1; i <= maxInflightPerSession; i++ {
		if got := call(i); got != http.StatusAccepted {
			t.Fatalf("request %d: status %d want 202", i, got)
		}
	}
	if got := call(100); got != http.StatusTooManyRequests {
		t.Fatalf("beyond the per-session limit the answer is 429, got %d", got)
	}
	close(block.release) // the held requests finish and free their places
	for i := 0; i < maxInflightPerSession; i++ {
		c.next(t)
	}
	deadline := time.Now().Add(2 * time.Second)
	for call(200) != http.StatusAccepted {
		if time.Now().After(deadline) {
			t.Fatal("the places must be given back")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheNumberOfClientsIsBounded(t *testing.T) {
	h := NewMCPHandler(nil)
	for i := 0; i < maxSessions; i++ {
		if _, err := h.OpenSession(); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if _, err := h.OpenSession(); err == nil {
		t.Fatal("past the limit a new client is refused")
	}
}

func TestAClientThatDoesNotReadIsDroppedNotWaitedFor(t *testing.T) {
	h := NewMCPHandler(nil)
	s, _ := h.OpenSession()
	for i := 0; i < sessionBuffer+1; i++ {
		s.send([]byte("x"))
	}
	select {
	case <-s.done:
	default:
		t.Fatal("a full buffer drops the session")
	}
	s.send([]byte("late")) // sending to a dropped session never blocks
}

func TestTwoWrappersOfTheSameCodeDoNotShareTheirAnchorOrTheirName(t *testing.T) {
	t.Setenv("BRIQUE_MCP_RESPONSE_CAP", "")
	t.Setenv("BRIQUE_WRAPPER_NAME", "")
	if got := wrapperNamesFromEnv(); got.responseCap != "mcp.response" || got.identity != "mcp_server_go" {
		t.Fatalf("default names: %+v", got)
	}
	t.Setenv("BRIQUE_MCP_RESPONSE_CAP", "mcp.reader.response")
	t.Setenv("BRIQUE_WRAPPER_NAME", "mcp_reader")
	if got := wrapperNamesFromEnv(); got.responseCap != "mcp.reader.response" || got.identity != "mcp_reader" {
		t.Fatalf("configured names: %+v", got)
	}
}
