// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The shared corpus of list parameters (wrapper/vectors/transit_lists.json): this runtime must agree with the Python and C++
// ones.
type listCorpus struct {
	InlineLimit int `json:"inline_limit_bytes"`
	Locality    struct {
		Bound [][]string `json:"bound"`
		Cases []struct {
			Context string `json:"context"`
			Cap     string `json:"cap"`
			Local   bool   `json:"local"`
		} `json:"cases"`
	} `json:"locality"`
	Emit []struct {
		Name   string `json:"name"`
		Target string `json:"target"`
		List   any    `json:"list"`
		Expect struct {
			Form     string   `json:"form"`
			Elements []string `json:"elements"`
		} `json:"expect"`
	} `json:"emit"`
	Receive []struct {
		Name     string           `json:"name"`
		Elements []map[string]any `json:"elements"`
		Expect   []any            `json:"expect"`
		Consumed *struct {
			Files   int `json:"files"`
			Objects int `json:"objects"`
		} `json:"consumed"`
	} `json:"receive"`
	Incoming []struct {
		Name    string              `json:"name"`
		Matters []map[string]any    `json:"matters"`
		Expect  map[string][]string `json:"expect"`
		Single  map[string]string   `json:"single"`
		Absent  []string            `json:"absent"`
	} `json:"incoming_matters"`
	Outgoing []struct {
		Name   string         `json:"name"`
		Params map[string]any `json:"params"`
		Expect struct {
			Matters map[string][]string `json:"matters"`
			Params  []string            `json:"params"`
			Error   string              `json:"error"`
		} `json:"expect"`
	} `json:"outgoing_matters"`
}

func loadListCorpus(t *testing.T) listCorpus {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "vectors", "transit_lists.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus listCorpus
	if err := json.Unmarshal(content, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.InlineLimit != transitInlineLimit {
		t.Fatalf("the corpus inline limit %d is not the runtime's %d", corpus.InlineLimit, transitInlineLimit)
	}
	return corpus
}

// makeValue builds a value from its spec (see the corpus).
func makeValue(spec any) any {
	switch v := spec.(type) {
	case map[string]any:
		if n, ok := v["text"].(float64); ok {
			return strings.Repeat("x", int(n))
		}
		if n, ok := v["object"].(float64); ok {
			return map[string]any{"k": strings.Repeat("x", int(n))}
		}
		if list, ok := v["list"].([]any); ok {
			return makeValue(list)
		}
		if rep, ok := v["repeat"]; ok {
			out := make([]any, int(v["times"].(float64)))
			for i := range out {
				out[i] = makeValue(rep)
			}
			return out
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = makeValue(item)
		}
		return out
	}
	return spec
}

func refKind(value any) string {
	ref, ok := value.(map[string]any)
	if !ok || !isTransitRef(ref) {
		return ""
	}
	switch {
	case ref["@object"] != nil:
		return "object"
	case ref["@file"] != nil:
		return "file"
	}
	return "matter"
}

func TestListVectorsLocality(t *testing.T) {
	corpus := loadListCorpus(t)
	r := testTransitRuntime(t)
	r.registry = NewRegistry()
	var bound []CapacityBinding
	for _, pair := range corpus.Locality.Bound {
		bound = append(bound, CapacityBinding{Context: pair[0], Name: pair[1], Handler: func(*Params) (any, error) { return nil, nil }})
	}
	if err := r.registry.Load(BindingsFragment{Capacities: bound}); err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus.Locality.Cases {
		t.Run(c.Context+" "+c.Cap, func(t *testing.T) {
			if got := r.transit.sameWrapper(Address{Context: c.Context, Cap: c.Cap, Type: TypeUser}); got != c.Local {
				t.Fatalf("local = %v, want %v", got, c.Local)
			}
		})
	}
}

func TestListVectorsEmit(t *testing.T) {
	corpus := loadListCorpus(t)
	for _, c := range corpus.Emit {
		t.Run(c.Name, func(t *testing.T) {
			r := testTransitRuntime(t)
			target := Address{Context: "/root", Cap: "consume", Type: TypeUser} // a capacity bound in this wrapper
			switch c.Target {
			case "instance":
				r.transit.runtime.registry = NewRegistry()
				target = Address{Context: "child", Cap: "consume", Type: TypeUser}
			case "external":
				target = Address{Context: "@ext_peer:/root/work", Cap: "consume", Type: TypeUser}
				created := 0
				r.transit.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					_, _ = io.ReadAll(request.Body)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"revision":2}`)), Header: make(http.Header)}, nil
				})}
				r.transit.matterInvoke = func(_ string, cap string, _ map[string]any, _ transitOrigin) (map[string]any, error) {
					switch cap {
					case "matter.create":
						created++
						return map[string]any{"matter_id": "transit-" + strings.Repeat("a", created), "revision": 1}, nil
					case "matter.write":
						return map[string]any{"http_data": map[string]any{"base_url": "http://matter.invalid", "path": "/upload", "tok": "secret"}}, nil
					}
					return map[string]any{"ok": true}, nil
				}
			}
			out, err := r.transit.Emit(makeValue(c.List), target, nil)
			if err != nil {
				t.Fatal(err)
			}
			form := refKind(out)
			var elements []string
			if form == "" {
				form = "inline"
				if items, ok := out.([]any); ok {
					for _, item := range items {
						kind := refKind(item)
						if kind == "" {
							kind = "inline"
						}
						elements = append(elements, kind)
					}
					for _, kind := range elements {
						if kind != "inline" {
							form = "elements"
						}
					}
				}
			}
			if form != c.Expect.Form {
				t.Fatalf("form %q, want %q (elements %v)", form, c.Expect.Form, elements)
			}
			if form == "elements" && !reflect.DeepEqual(elements, c.Expect.Elements) {
				t.Fatalf("elements %v, want %v", elements, c.Expect.Elements)
			}
		})
	}
}

func TestListVectorsReceive(t *testing.T) {
	corpus := loadListCorpus(t)
	for _, c := range corpus.Receive {
		t.Run(c.Name, func(t *testing.T) {
			r := testTransitRuntime(t)
			files, objects := 0, 0
			raw := make([]any, 0, len(c.Elements))
			for _, element := range c.Elements {
				switch {
				case element["file"] != nil:
					data, _ := json.Marshal(element["file"])
					ref, err := r.transit.writeFile(data, "json")
					if err != nil {
						t.Fatal(err)
					}
					raw = append(raw, ref)
					files++
				case element["object"] != nil:
					id := "obj_vector_" + string(rune('a'+objects))
					r.transit.objects[id] = element["object"]
					raw = append(raw, map[string]any{"@object": id, "wrapper": r.wrapperName, "type": ""})
					objects++
				default:
					raw = append(raw, element["plain"])
				}
			}
			params := newParams(map[string]any{"parts": raw}, r, nil)
			for i := 0; i < 2; i++ { // the second read is the memoized one
				got, err := params.Value("parts")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, any(c.Expect)) {
					t.Fatalf("got %#v, want %#v", got, c.Expect)
				}
			}
			if err := params.releaseUnconsumed(); err != nil {
				t.Fatal(err)
			}
			left, _ := os.ReadDir(r.transit.directory)
			if len(left) != 0 || len(r.transit.objects) != 0 {
				t.Fatalf("references left behind: %d files, %d objects", len(left), len(r.transit.objects))
			}
			if c.Consumed != nil && (c.Consumed.Files != files || c.Consumed.Objects != objects) {
				t.Fatalf("the case says %+v consumed, the elements hold %d files and %d objects", *c.Consumed, files, objects)
			}
		})
	}
}

func TestListVectorsIncomingMatters(t *testing.T) {
	corpus := loadListCorpus(t)
	for _, c := range corpus.Incoming {
		t.Run(c.Name, func(t *testing.T) {
			r := testTransitRuntime(t)
			params := newParams(map[string]any{"@matter": incomingMatterParams(c.Matters)}, r, nil)
			for name, want := range c.Expect {
				refs, err := params.Matters(name)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, len(refs))
				for i, ref := range refs {
					got[i] = ref.Context + "/" + ref.ID
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: got %v, want %v", name, got, want)
				}
			}
			for name, want := range c.Single {
				ref, err := params.Matter(name)
				if want == "type_mismatch" {
					if te, ok := err.(*TransitError); !ok || te.Reason != "type_mismatch" {
						t.Fatalf("%s: want type_mismatch, got %v", name, err)
					}
					continue
				}
				if err != nil || ref.Context+"/"+ref.ID != want {
					t.Fatalf("%s: got %v %v, want %s", name, ref, err, want)
				}
			}
			for _, name := range c.Absent {
				if _, err := params.Matters(name); err == nil || err.(*TransitError).Reason != "not_found" {
					t.Fatalf("%s: want not_found, got %v", name, err)
				}
			}
		})
	}
}

func TestListVectorsOutgoingMatters(t *testing.T) {
	corpus := loadListCorpus(t)
	convert := func(value any) any {
		var one func(any) any
		one = func(v any) any {
			if obj, ok := v.(map[string]any); ok {
				if text, ok := obj["matter"].(string); ok {
					cut := strings.LastIndex(text, "/")
					return MatterRef{Context: text[:cut], ID: text[cut+1:]}
				}
			}
			if list, ok := v.([]any); ok {
				out := make([]any, len(list))
				for i, item := range list {
					out[i] = one(item)
				}
				return out
			}
			return v
		}
		return one(value)
	}
	for _, c := range corpus.Outgoing {
		t.Run(c.Name, func(t *testing.T) {
			matters := map[string][]string{}
			var rest []string
			for _, key := range sortedKeys(c.Params) {
				entries, isMatters, err := matterEntries(key, convert(c.Params[key]))
				if c.Expect.Error != "" {
					if err == nil || err.(*TransitError).Reason != c.Expect.Error {
						t.Fatalf("want %s, got %v", c.Expect.Error, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !isMatters {
					rest = append(rest, key)
					continue
				}
				for _, e := range entries {
					repr := e["repr"].(string)
					matters[repr] = append(matters[repr], e["context"].(string)+"/"+e["id"].(string))
				}
			}
			if !reflect.DeepEqual(matters, c.Expect.Matters) {
				t.Fatalf("matters %v, want %v", matters, c.Expect.Matters)
			}
			if len(rest) != len(c.Expect.Params) {
				t.Fatalf("params %v, want %v", rest, c.Expect.Params)
			}
		})
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
