package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
