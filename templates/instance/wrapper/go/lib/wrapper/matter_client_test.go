// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type matterVectors struct {
	Substance []struct {
		Name   string         `json:"name"`
		Data   any            `json:"data"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	} `json:"substance"`
	Revision []struct {
		Brique   any   `json:"brique"`
		Revision int64 `json:"revision"`
	} `json:"revision"`
	Lines []struct {
		Name  string           `json:"name"`
		Text  any              `json:"text"`
		Lines []map[string]any `json:"lines"`
		Error string           `json:"error"`
	} `json:"lines"`
	DumpLines []struct {
		Name  string           `json:"name"`
		Lines []map[string]any `json:"lines"`
		Text  string           `json:"text"`
	} `json:"dump_lines"`
	Append struct {
		InlineMaxBytes int `json:"inline_max_bytes"`
	} `json:"append"`
	Pause struct {
		BaseMs int `json:"base_ms"`
		CapMs  int `json:"cap_ms"`
		Cases  []struct {
			Attempt int     `json:"attempt"`
			MaxMs   float64 `json:"max_ms"`
		} `json:"cases"`
	} `json:"guarded_write_pause"`
}

func loadMatterVectors(t *testing.T) matterVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../vectors/matter_client.json")
	if err != nil {
		t.Fatal(err)
	}
	var v matterVectors
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber() // a revision above 2^53 keeps its digits, as the transport keeps it
	if err := decoder.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func causeOf(err *CallError) string {
	cause, _ := err.Details["cause"].(string)
	return cause
}

func TestMatterClientVectors(t *testing.T) {
	v := loadMatterVectors(t)
	for _, c := range v.Substance {
		got, err := classifySubstance(c.Data)
		if c.Error != "" {
			if err == nil || causeOf(err) != c.Error {
				t.Errorf("%s: want error %s, got %v", c.Name, c.Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		want := c.Result["text"]
		got2 := any(got.text)
		if c.Result["kind"] == "lease" {
			want, got2 = c.Result["url"], got.url
		}
		if got.kind != c.Result["kind"] || got2 != want {
			t.Errorf("%s: got %+v", c.Name, got)
		}
	}
	for _, c := range v.Revision {
		if got := revisionOf(normalizeNumbers(c.Brique)); got != c.Revision {
			t.Errorf("revision of %v = %d, want %d", c.Brique, got, c.Revision)
		}
	}
	for _, c := range v.Lines {
		got, err := parseLines(c.Text)
		if c.Error != "" {
			if err == nil || causeOf(err) != c.Error {
				t.Errorf("%s: want error %s, got %v", c.Name, c.Error, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(roundTrip(got), roundTrip(c.Lines)) {
			t.Errorf("%s: got %v (%v)", c.Name, got, err)
		}
	}
	for _, c := range v.DumpLines {
		got, err := dumpLines(c.Lines)
		if err != nil || got != c.Text {
			t.Errorf("%s: got %q, want %q", c.Name, got, c.Text)
		}
	}
	if InlineAppendMax != v.Append.InlineMaxBytes {
		t.Errorf("inline append limit %d, want %d", InlineAppendMax, v.Append.InlineMaxBytes)
	}
	for _, c := range v.Pause.Cases {
		high := float64(guardedWritePause(c.Attempt, 1, v.Pause.BaseMs, v.Pause.CapMs).Microseconds()) / 1000
		if high != c.MaxMs || guardedWritePause(c.Attempt, 0, v.Pause.BaseMs, v.Pause.CapMs) != 0 {
			t.Errorf("attempt %d: pause up to %v ms, want %v", c.Attempt, high, c.MaxMs)
		}
	}
}

func roundTrip(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestExactRevisionsSurviveTheTransport(t *testing.T) {
	raw := []byte(`{"kind":"response","response":{"intention_id":"i","status":"ok","payload":{"brique":{"revision":1790000000000000001,"size":12},"total_lines":3}}}`)
	env, _, err := decodeWrapperFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	brique := env.Response.Payload["brique"].(map[string]any)
	if got := revisionOf(brique); got != 1790000000000000001 {
		t.Fatalf("revision %d lost its digits", got)
	}
	if _, isFloat := brique["size"].(float64); !isFloat {
		t.Fatalf("a small number must stay a float64, got %T", brique["size"])
	}
	framed := []byte(`{"brique_wrapper_frame":"brique.wrapper/1","message":` + string(raw) + `,"delegation":"` + strings.Repeat("A", 43) + `"}`)
	env, _, err = decodeWrapperFrame(framed)
	if err != nil {
		t.Fatal(err)
	}
	if got := revisionOf(env.Response.Payload["brique"]); got != 1790000000000000001 {
		t.Fatalf("framed revision %d lost its digits", got)
	}
}

type recorded struct {
	cap, context string
	params       map[string]any
}

type scripted struct {
	mu      sync.Mutex
	answers []any // map[string]any payload, or error
	calls   []recorded
}

func (s *scripted) call(cap string, params map[string]any, context string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recorded{cap, context, params})
	answer := s.answers[0]
	s.answers = s.answers[1:]
	if err, isErr := answer.(error); isErr {
		return nil, err
	}
	return answer.(map[string]any), nil
}

func revisionRefused() error {
	return &CallError{Code: "refused", Message: "x", Details: map[string]any{"reason": "revision_mismatch"}}
}

func TestReadInlineThroughALeaseAndStale(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		io.WriteString(w, `{"big":true}`)
	}))
	defer server.Close()
	lease := map[string]any{"kind": "http", "http": map[string]any{"base_url": server.URL, "path": "/substance/x", "tok": "t"}}
	script := &scripted{answers: []any{
		map[string]any{"data": lease, "brique": map[string]any{"revision": json.Number("1")}},
		map[string]any{"data": lease, "brique": map[string]any{"revision": json.Number("2")}},
	}}
	client := newMatterClient(script.call, "/ctx")
	view, err := client.Read("m1")
	if err != nil || view.Revision != 2 || view.Content["big"] != true || len(script.calls) != 2 {
		t.Fatalf("read: %+v %v (%d calls)", view, err, len(script.calls))
	}
	inline := &scripted{answers: []any{map[string]any{"data": map[string]any{"kind": "inline", "bytes": `{"a": 1}`}, "brique": map[string]any{"revision": 7.0}, "functional": map[string]any{"f": 1.0}}}}
	view, err = newMatterClient(inline.call, "/ctx").Read("m1")
	if err != nil || view.Revision != 7 || inline.calls[0].context != "/ctx" || inline.calls[0].cap != "matter.read" {
		t.Fatalf("inline read: %+v %v", view, err)
	}
}

func TestWriteIsGuardedByADecimalRevision(t *testing.T) {
	revision := int64(1790000000000000001)
	script := &scripted{answers: []any{map[string]any{"revision": json.Number("1790000000000000009")}, map[string]any{"revision": 10.0}}}
	client := newMatterClient(script.call, "/ctx")
	got, err := client.Write("m1", map[string]any{"a": 1}, &revision)
	if err != nil || got != 1790000000000000009 || script.calls[0].params["expected_revision"] != "1790000000000000001" {
		t.Fatalf("write: %d %v %v", got, err, script.calls[0].params)
	}
	if _, err = client.Write("m1", map[string]any{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, guarded := script.calls[1].params["expected_revision"]; guarded {
		t.Fatal("a write with no guard must not carry expected_revision")
	}
}

func TestAppendInlineAndThroughALease(t *testing.T) {
	script := &scripted{answers: []any{map[string]any{"total_lines": 3.0}}}
	client := newMatterClient(script.call, "/ctx")
	total, err := client.AppendLines("j", []map[string]any{{"a": 1}}, 2)
	if err != nil || total != 3 || script.calls[0].params["data"] != "{\"a\":1}\n" {
		t.Fatalf("append inline: %d %v %v", total, err, script.calls[0].params)
	}
	var received []byte
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer server.Close()
	handle := map[string]any{"http": map[string]any{"base_url": server.URL, "path": "/append/x", "tok": "t"}}
	big := make([]map[string]any, 300)
	for i := range big {
		big[i] = map[string]any{"k": strings.Repeat("x", 1000)}
	}
	script = &scripted{answers: []any{map[string]any{"http_data": handle}, map[string]any{"http_data": handle}}}
	client = newMatterClient(script.call, "/ctx")
	total, err = client.AppendLines("j", big, 5)
	want, _ := dumpLines(big)
	if err != nil || total != 305 || script.calls[0].params["http_data"] != true || string(received) != want {
		t.Fatalf("append lease: %d %v", total, err)
	}
	status = http.StatusConflict
	if _, err = client.AppendLines("j", big, 5); !IsLineCountMismatch(err) {
		t.Fatalf("a conflicting append lease must be a line count mismatch, got %v", err)
	}
}

func TestReadLines(t *testing.T) {
	script := &scripted{answers: []any{map[string]any{"data": map[string]any{"kind": "inline", "bytes": "{\"a\":1}\n{\"b\":2}\n", "total_lines": 9.0}, "brique": map[string]any{"revision": 4.0}}}}
	view, err := newMatterClient(script.call, "/ctx").ReadLines("j", 7, 2)
	if err != nil || len(view.Lines) != 2 || view.TotalLines != 9 || view.Revision != 4 || view.Offset != 7 {
		t.Fatalf("read lines: %+v %v", view, err)
	}
}

func TestUpdateStartsAgainAfterALostRace(t *testing.T) {
	t.Setenv("BRIQUE_CAS_PAUSE_BASE_MS", "0")
	read := func(n string, revision float64) map[string]any {
		return map[string]any{"data": map[string]any{"bytes": `{"n": ` + n + `}`}, "brique": map[string]any{"revision": revision}}
	}
	script := &scripted{answers: []any{read("1", 1), revisionRefused(), read("2", 2), map[string]any{"revision": 3.0}}}
	result, err := newMatterClient(script.call, "/ctx").Update("m1", func(content map[string]any) (map[string]any, any, error) {
		n := content["n"].(float64)
		return map[string]any{"n": n + 1}, n, nil
	})
	if err != nil || result != 2.0 {
		t.Fatalf("update: %v %v", result, err)
	}
	last := script.calls[len(script.calls)-1].params
	if last["expected_revision"] != "2" || !strings.Contains(last["data"].(string), `"n": 3`) {
		t.Fatalf("the retry must write on the second read: %v", last)
	}
}

func TestUpdateWithNothingToWriteAndGivingUp(t *testing.T) {
	script := &scripted{answers: []any{map[string]any{"data": map[string]any{"bytes": `{"n": 1}`}, "brique": map[string]any{"revision": 1.0}}}}
	result, err := newMatterClient(script.call, "/ctx").Update("m1", func(map[string]any) (map[string]any, any, error) { return nil, "already", nil })
	if err != nil || result != "already" || len(script.calls) != 1 {
		t.Fatalf("nothing to write: %v %v %d", result, err, len(script.calls))
	}
	t.Setenv("BRIQUE_CAS_PAUSE_BASE_MS", "0")
	t.Setenv("BRIQUE_CAS_ATTEMPTS", "3")
	var answers []any
	for i := 0; i < 3; i++ {
		answers = append(answers, map[string]any{"data": map[string]any{"bytes": "{}"}, "brique": map[string]any{"revision": 1.0}}, revisionRefused())
	}
	script = &scripted{answers: answers}
	_, err = newMatterClient(script.call, "/ctx").Update("m1", func(map[string]any) (map[string]any, any, error) { return map[string]any{"x": 1}, nil, nil })
	if !errors.Is(err, ErrConcurrencyExhausted) || len(script.calls) != 6 {
		t.Fatalf("giving up: %v (%d calls)", err, len(script.calls))
	}
}
