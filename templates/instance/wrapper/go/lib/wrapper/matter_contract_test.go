// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMatterRefSerializesToTheWireForm(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"doc": MatterRef{Context: "/root/a", ID: "report", Mode: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"doc":{"context":"/root/a","id":"report","mode":"read"}}` {
		t.Fatalf("unexpected wire form: %s", raw)
	}
	raw, _ = json.Marshal(MatterRef{Context: "/r", ID: "x"})
	if string(raw) != `{"context":"/r","id":"x"}` {
		t.Fatalf("empty mode must be omitted: %s", raw)
	}
}

func TestIncomingMattersUseReprOrIDAndSkipIncompleteEntries(t *testing.T) {
	got := incomingMatterParams([]map[string]any{
		{"context": "/root/demo", "id": "state", "repr": "src", "mode": "read"},
		{"context": " /root/demo ", "id": " state "}, // repr missing: the id names it
		{"context": "", "id": "orphan", "repr": "bad"},
		{"context": "/root/demo", "id": 7, "repr": "typed"},
	})
	want := map[string]any{
		"src":   map[string]any{"context": "/root/demo", "id": "state", "mode": "read"},
		"state": map[string]any{"context": "/root/demo", "id": "state"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incoming matters = %#v, want %#v", got, want)
	}
}

func TestParamsMatterFromIncomingAndFromResultPayload(t *testing.T) {
	r := testTransitRuntime(t)
	incoming := newParams(map[string]any{"@matter": incomingMatterParams([]map[string]any{
		{"context": "/root/demo", "id": "state", "repr": "st", "mode": "read"},
	})}, r, nil)
	ref, err := incoming.Matter("st")
	if err != nil || ref != (MatterRef{Context: "/root/demo", ID: "state", Mode: "read"}) {
		t.Fatalf("incoming Matter = %#v, %v", ref, err)
	}
	// value() agrees with matter()
	v, err := incoming.Value("st")
	if err != nil || v != any(ref) {
		t.Fatalf("Value = %#v, %v", v, err)
	}

	result := newParams(map[string]any{
		"doc":  map[string]any{"context": "/root/a", "id": "report", "mode": "read"},
		"bare": map[string]any{"context": "/r", "id": "x"},
	}, r, nil)
	if ref, err := result.Matter("doc"); err != nil || ref != (MatterRef{Context: "/root/a", ID: "report", Mode: "read"}) {
		t.Fatalf("payload Matter = %#v, %v", ref, err)
	}
	if ref, err := result.Matter("bare"); err != nil || ref.Mode != "" {
		t.Fatalf("mode is optional: %#v, %v", ref, err)
	}
}

func TestParamsMatterRejectsWhatIsNotExactlyAReference(t *testing.T) {
	r := testTransitRuntime(t)
	params := newParams(map[string]any{
		"extra":    map[string]any{"context": "/r", "id": "x", "title": "business data"},
		"no_id":    map[string]any{"context": "/r"},
		"empty_id": map[string]any{"context": "/r", "id": " "},
		"bad_mode": map[string]any{"context": "/r", "id": "x", "mode": 3},
		"scalar":   "text",
		"file":     map[string]any{"@file": "/x/y", "format": "json"},
	}, r, nil)
	for _, name := range []string{"extra", "no_id", "empty_id", "bad_mode", "scalar", "file"} {
		_, err := params.Matter(name)
		var terr *TransitError
		if !errors.As(err, &terr) || terr.Reason != "type_mismatch" {
			t.Fatalf("%s: error = %v, want type_mismatch", name, err)
		}
	}
	_, err := params.Matter("missing")
	var terr *TransitError
	if !errors.As(err, &terr) || terr.Reason != "not_found" {
		t.Fatalf("missing: error = %v, want not_found", err)
	}
}
