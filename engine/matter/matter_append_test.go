/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/circulation"
)

func createAppendFixture(t *testing.T, framing, payload string) (*MatterLoop, chan circulation.Message, string, int64) {
	t.Helper()
	l, comm, root := newMatterLoopHarness(t)
	l.capMatterCreate(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "create-append",
		Params: map[string]any{
			circulation.KeyMatterID: "log",
			circulation.KeyMatter: map[string]any{
				circulation.KeyBrique:     map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique, circulation.KeySubstanceFraming: framing},
				circulation.KeyFunctional: map[string]any{circulation.KeyFormat: "log"},
			},
			circulation.KeyPayload: payload,
		},
	}})
	response := recvMatterLoopMsg(t, comm, "create append fixture")
	if response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("create failed: %#v", response)
	}
	return l, comm, root, response.Response.Payload[circulation.KeyRevision].(int64)
}

func TestMatterAppendBytesAndGuards(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingBytes, "abc")
	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "append",
		Params: map[string]any{
			circulation.KeyMatterID:         "log",
			circulation.KeyData:             "def",
			circulation.KeyExpectedRevision: revision,
			circulation.KeyExpectedByteSize: int64(3),
		},
	}})
	response := recvMatterLoopMsg(t, comm, "append")
	if response.Response.Status != circulation.ValueStatusOK || response.Response.Payload[circulation.KeyByteOffset] != int64(3) || response.Response.Payload[circulation.KeyTotalBytes] != int64(6) {
		t.Fatalf("append response: %#v", response)
	}
	bytes, err := os.ReadFile(filepath.Join(root, matterDirName, "log.log"))
	if err != nil || string(bytes) != "abcdef" {
		t.Fatalf("payload=%q err=%v", bytes, err)
	}

	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "append-stale", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyData: "again", circulation.KeyExpectedByteSize: int64(3),
		},
	}})
	stale := recvMatterLoopMsg(t, comm, "stale append")
	if stale.Response.Status != circulation.ValueStatusError {
		t.Fatalf("stale append should fail: %#v", stale)
	}
}

func TestMatterAppendLines(t *testing.T) {
	l, comm, _, revision := createAppendFixture(t, circulation.ValueFramingLines, "one\n")
	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "append-lines", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyData: "two\nthree\n",
			circulation.KeyExpectedRevision: revision, circulation.KeyExpectedLineCount: int64(1),
		},
	}})
	response := recvMatterLoopMsg(t, comm, "append lines")
	if response.Response.Status != circulation.ValueStatusOK || response.Response.Payload[circulation.KeyLineOffset] != int64(1) || response.Response.Payload[circulation.KeyTotalLines] != int64(3) {
		t.Fatalf("line append response: %#v", response)
	}
}

func TestMatterAppendHTTPAndStaleCompetingLease(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingLines, "one\n")
	l.subHTTP.srvMu.Lock()
	l.subHTTP.baseURL = "http://127.0.0.1:1"
	l.subHTTP.srvMu.Unlock()

	open := func(id string) SubstanceHTTPHandle {
		l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: id, Params: map[string]any{
				circulation.KeyMatterID: "log", circulation.KeyHTTPData: true,
				circulation.KeyExpectedRevision: revision, circulation.KeyExpectedByteSize: int64(4),
			},
		}})
		response := recvMatterLoopMsg(t, comm, "open HTTP append")
		if response.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("open HTTP append failed: %#v", response)
		}
		handle, ok := response.Response.Payload[circulation.KeyHTTPData].(SubstanceHTTPHandle)
		if !ok {
			t.Fatalf("unexpected HTTP handle: %#v", response.Response.Payload[circulation.KeyHTTPData])
		}
		return handle
	}
	first, second := open("append-http-1"), open("append-http-2")
	put := func(handle SubstanceHTTPHandle, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, handle.Path+"?tok="+handle.Tok, strings.NewReader(body))
		l.subHTTP.handleSubstance(recorder, request)
		return recorder
	}
	firstResponse := put(first, "two\n")
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first append status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &result); err != nil || result[circulation.KeyTotalBytes] != float64(8) || result[circulation.KeyTotalLines] != float64(2) {
		t.Fatalf("first append result=%#v err=%v", result, err)
	}
	secondResponse := put(second, "stale\n")
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("competing stale append status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	payload, err := os.ReadFile(filepath.Join(root, matterDirName, "log.log"))
	if err != nil || string(payload) != "one\ntwo\n" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}
