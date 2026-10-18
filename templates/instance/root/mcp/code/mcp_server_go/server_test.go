package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandleMessageRejectsBulkPayload(t *testing.T) {
	server := NewMCPServer("", NewMCPHandler(nil))
	body := `{"jsonrpc":"2.0","method":"x","params":"` + strings.Repeat("x", int(maxControlMessageBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleMessage(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

// longOutbound is a fake Brique outbound whose capacity acknowledges itself `running` twice before answering.
type longOutbound struct{ plainCalls int }

func (o *longOutbound) Initialize() (map[string]any, error) { return nil, nil }
func (o *longOutbound) CognitiveFramework() (string, error) { return "", nil }
func (o *longOutbound) ForwardIntention(map[string]any) (map[string]any, error) {
	o.plainCalls++
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"final":true}`}}}, nil
}
func (o *longOutbound) ForwardIntentionWithProgress(_ map[string]any, onRunning func(int, time.Duration)) (map[string]any, error) {
	onRunning(1, 10*time.Second)
	onRunning(2, 20*time.Second)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"final":true}`}}}, nil
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
	rec := &syncRecorder{rec: httptest.NewRecorder()}
	done := make(chan struct{})
	go h.RegisterSSE(rec, rec, done)
	time.Sleep(20 * time.Millisecond)
	h.Dispatch(JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "tools/call", Params: json.RawMessage(params)})
	close(done)
	for _, line := range strings.Split(rec.body(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	return frames, outbound
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
