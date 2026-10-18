// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func testTransitRuntime(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "context.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIQUE_CTX_DIR", root)
	t.Setenv("BRIQUE_CTX_ID", "/root")
	t.Setenv("BRIQUE_WRAPPER_NAME", "test")
	r := NewRuntime()
	r.registry = NewRegistry()
	if err := r.registry.Load(BindingsFragment{Capacities: []CapacityBinding{{Context: "", Name: "consume", Handler: func(*Params) (any, error) { return nil, nil }}}}); err != nil {
		t.Fatal(err)
	}
	service, err := NewTransitService(r)
	if err != nil {
		t.Fatal(err)
	}
	r.transit = service
	return r
}

func TestTransientMatterRoundTripUsesLeaseAndGuardedDelete(t *testing.T) {
	r := testTransitRuntime(t)
	payload := []byte(`{"large":[1,2,3]}`)
	var uploaded []byte
	r.transit.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPut {
			t.Fatalf("unexpected HTTP method %s", request.Method)
		}
		uploaded, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"revision":2}`)), Header: make(http.Header)}, nil
	})}
	var calls []string
	r.transit.matterInvoke = func(_ string, cap string, params map[string]any, _ transitOrigin) (map[string]any, error) {
		calls = append(calls, cap)
		switch cap {
		case "matter.create":
			return map[string]any{"matter_id": "transit-1", "revision": 1}, nil
		case "matter.write":
			return map[string]any{"http_data": map[string]any{"base_url": "http://matter.invalid", "path": "/upload", "tok": "secret"}}, nil
		case "matter.read":
			return map[string]any{"brique": map[string]any{"transient": true, "revision": 2}, "data": map[string]any{"kind": "inline", "encoding": "base64", "bytes": base64.StdEncoding.EncodeToString(payload)}}, nil
		case "matter.delete":
			if asInt(params["expected_revision"]) != 2 || asInt(params["expected_byte_size"]) != len(payload) {
				t.Fatalf("unguarded delete: %#v", params)
			}
			return map[string]any{"ok": true}, nil
		default:
			t.Fatalf("unexpected capacity %s", cap)
		}
		return nil, nil
	}
	ref, err := r.transit.createMatter(payload, "json", Address{Context: "@ext_peer:/root/work", Cap: "consume", Type: TypeUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(uploaded) != string(payload) {
		t.Fatalf("uploaded %q", uploaded)
	}
	value, err := r.transit.resolve(ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["large"].([]any)[2].(float64) != 3 {
		t.Fatalf("unexpected decoded value: %#v", value)
	}
	if got := calls; len(got) != 4 || got[0] != "matter.create" || got[1] != "matter.write" || got[2] != "matter.read" || got[3] != "matter.delete" {
		t.Fatalf("unexpected calls: %v", got)
	}
}

func TestTransitObjectIsTakenOnce(t *testing.T) {
	r := testTransitRuntime(t)
	ref, err := r.transit.Emit(map[string]any{"large": true}, Address{Context: "/root", Cap: "consume", Type: TypeUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := r.transit.resolve(ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["large"] != true {
		t.Fatalf("unexpected value: %#v", value)
	}
	if _, err := r.transit.resolve(ref, nil); err == nil {
		t.Fatal("second take should fail")
	}
}

func TestTransitFileIsDeletedAfterDecode(t *testing.T) {
	r := testTransitRuntime(t)
	r.transit.runtime.registry = NewRegistry() // force a different wrapper target
	value := map[string]any{"payload": string(make([]byte, transitInlineLimit+1))}
	ref, err := r.transit.Emit(value, Address{Context: "child", Cap: "consume", Type: TypeUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := ref.(map[string]any)["@file"].(string)
	decoded, err := r.transit.resolve(ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.(map[string]any)["payload"] != value["payload"] {
		t.Fatal("decoded value differs")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("transit file remains: %v", err)
	}
}

func TestLargeTextScalarUsesTransitFile(t *testing.T) {
	r := testTransitRuntime(t)
	r.transit.runtime.registry = NewRegistry()
	value := strings.Repeat("x", transitInlineLimit+1)
	ref, err := r.transit.Emit(value, Address{Context: "child", Cap: "consume", Type: TypeUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref.(map[string]any)["format"] != "text" {
		t.Fatalf("unexpected reference: %#v", ref)
	}
	decoded, err := r.transit.resolve(ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != value {
		t.Fatal("decoded text differs")
	}
}

func TestMatterWithBusinessSiblingIsNotTransit(t *testing.T) {
	if isTransitRef(map[string]any{"@matter": "state", "context": "/root", "consume": true, "format": "json", "revision": 1, "size": 2, "business": true}) {
		t.Fatal("an object with business fields must not become a transit reference")
	}
}
