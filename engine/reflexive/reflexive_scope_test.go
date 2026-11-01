/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package reflexive

// reflexive_scope_test.go
//
// Result filtering (SECURITY.md §4.5): what an external caller receives
// from meaning.query, read.structure, read.state, trace.inspect and
// vocabulary.*. The expected answers come from the policy SOURCE evaluator
// (policysrc), not from the code under test: a source is compiled, signed,
// activated, and the handlers are called as the reviewed door would.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

type scopeElement struct{ ctx, kind, name string }

// The synthetic instance. Names are chosen so a leak is greppable.
var scopeContexts = []string{"/root", "/root/catalog", "/root/catalog/public", "/root/catalog/secret", "/root/other", "/root/hidden"}

var scopeElements = []scopeElement{
	{"/root", "capacity", "cap_root"}, {"/root", "capacity", "cap_hole"}, {"/root", "document", "doc_hole"},
	{"/root/catalog", "capacity", "cap_catalog"}, {"/root/catalog", "document", "doc_catalog"},
	{"/root/catalog", "schema", "sch_catalog"}, {"/root/catalog", "matter", "mat_catalog"},
	{"/root/catalog", "matter", "mat_catalog_private"}, {"/root/catalog", "structure", "str_catalog"},
	{"/root/catalog/public", "capacity", "cap_public"}, {"/root/catalog/public", "matter", "mat_public"},
	{"/root/catalog/secret", "capacity", "cap_secret"}, {"/root/catalog/secret", "matter", "mat_secret"},
	{"/root/other", "capacity", "cap_other"}, {"/root/other", "matter", "mat_other"},
	{"/root/hidden", "capacity", "cap_hidden"},
}

// scopePrincipals: every source form that matters for filtering.
var scopePrincipals = map[string][]policysrc.Grant{
	"catalog": {
		{Read: policysrc.TargetList{"/root/catalog/**"}, Except: []string{"/root/catalog/secret/**"}},
	},
	"everything":  {{Read: policysrc.TargetList{"/root/**"}}},
	"single":      {{Read: policysrc.TargetList{"/root/catalog/public"}}},
	"named":       {{Read: policysrc.TargetList{"/root/other#matter:mat_other"}}},
	"promoted":    {{Read: policysrc.TargetList{"/root/**"}, Except: []string{"/root/catalog"}}},
	"matters":     {{Read: policysrc.TargetList{"/root/**#matter:*"}}},
	"private-cut": {{Read: policysrc.TargetList{"/root/**"}, Except: []string{"/root/catalog#matter:mat_catalog_private"}}},
	"nobody":      {},
	// "holes": everything, except named elements and one context. Used by the
	// refusal tests: the excepted elements exist, their *_ghost twins do not.
	"holes": {
		{Read: policysrc.TargetList{"/root/**"}, Except: refusalHoles},
		{Define: policysrc.TargetList{"/root/**"}, Except: refusalHoles},
	},
	"rootonly": {{Read: policysrc.TargetList{"/root"}}},
	"page":     {{Read: policysrc.TargetList{"/root/other"}, Except: pageExceptions()}},
}

// pageExceptions removes thirty matters of /root/other that sort before the one
// the "page" principal can read.
func pageExceptions() []string {
	out := make([]string, 0, scopeUnreadablePage)
	for i := 0; i < scopeUnreadablePage; i++ {
		out = append(out, fmt.Sprintf("/root/other#matter:aaa_unreadable_%02d", i))
	}
	return out
}

const scopeUnreadablePage = 30

var refusalHoles = []string{
	"/root#capacity:cap_hole", "/root#capacity:cap_hole_ghost", "/root#document:doc_hole", "/root#document:doc_hole_ghost", "/root/hidden",
}

func init() {
	for i := 0; i < scopeUnreadablePage; i++ {
		scopeElements = append(scopeElements, scopeElement{"/root/other", "matter", fmt.Sprintf("aaa_unreadable_%02d", i)})
	}
}

type scopeFixture struct {
	t        *testing.T
	now      time.Time
	src      *policysrc.Source
	keys     map[string]ed25519.PrivateKey
	keyIDs   map[string]string
	registry *junction.ContextCommRegistry
	runtime  *trust.Runtime
	loop     *ReflexiveLoop
	comm     chan junction.Delivery
	root     string
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := &scopeFixture{t: t, now: time.UnixMilli(1792843200000).UTC(), keys: map[string]ed25519.PrivateKey{}, keyIDs: map[string]string{}}
	f.src = &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "scope-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{}}
	for name, grants := range scopePrincipals {
		public, private, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		keyID, _ := trust.SigningKeyID(public)
		f.keys[name], f.keyIDs[name] = private, keyID
		f.src.Principals[name] = &policysrc.Principal{
			Kind: "instance", Keys: []policysrc.Key{{PublicKey: base64.RawURLEncoding.EncodeToString(public)}},
			Grants: grants, TracePayloads: name == "everything",
		}
	}
	f.activate()
	f.buildInstance()
	return f
}

func (f *scopeFixture) activate() {
	t := f.t
	result, err := policysrc.Compile(f.src, trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()},
		policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	raw, _ := json.Marshal(result.Policy)
	canonical, err := trust.CanonicalPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := trust.NewRuntime("scope-instance", "main")
	if err != nil {
		t.Fatal(err)
	}
	catalogForRuntime := trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()}
	candidate, err := runtime.Prepare(decoded, catalogForRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.runtime = runtime
	f.registry = junction.NewContextCommRegistry()
	if err := f.registry.InstallTrustRuntime(runtime); err != nil {
		t.Fatal(err)
	}
}

func (f *scopeFixture) buildInstance() {
	t := f.t
	f.root = t.TempDir()
	put := func(rel, content string) {
		p := filepath.Join(f.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	children := map[string][]string{"/root": {"catalog", "other", "hidden"}, "/root/catalog": {"public", "secret"}}
	for _, ctx := range scopeContexts {
		rel := strings.TrimPrefix(strings.TrimPrefix(ctx, "/root"), "/")
		list, _ := json.Marshal(children[ctx])
		if children[ctx] == nil {
			list = []byte("[]")
		}
		put(filepath.ToSlash(filepath.Join(rel, "context.json")),
			fmt.Sprintf(`{"brique":{"ctx_name":%q,"children_list":%s}}`, filepath.Base(ctx), list))
	}
	for _, e := range scopeElements {
		rel := strings.TrimPrefix(strings.TrimPrefix(e.ctx, "/root"), "/")
		switch e.kind {
		case "matter":
			put(filepath.ToSlash(filepath.Join(rel, "matter", e.name+MatterDescriptorExt)), fmt.Sprintf(`{"brique":{"name":%q},"objective":{"name":%q}}`, e.name, e.name))
		case "structure":
			put(filepath.ToSlash(filepath.Join(rel, "structure", e.name+StructureDescriptorExt)), fmt.Sprintf(`{"brique":{"name":%q},"objective":{"name":%q}}`, e.name, e.name))
		default:
			put(filepath.ToSlash(filepath.Join(rel, e.kind, e.name+".json")),
				fmt.Sprintf(`{"brique":{"cap_name":%q,"kind":"dsl"},"objective":{"name":%q,"description":"d"},"functional":{"role":"r"},"subjective":{}}`, e.name, e.name))
		}
	}
	// Implementation directories: not a resource genre, never shown externally.
	put("catalog/code/secret_impl.js", "// implementation")
	put("catalog/ui/secret_view.html", "<html></html>")
	f.comm = make(chan junction.Delivery, 64)
	frame := &junction.ContextRegistry{
		ContextDir: f.root, CtxId: shared.RootContextID, CtxCommReg: f.registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: f.comm, shared.FamilyTrace: make(chan junction.Delivery, 16)},
	}
	frame.SetLocalAuthority(junction.MustLocalAuthority("context:/root"))
	f.loop = NewReflexiveLoop(frame, nil)
	t.Cleanup(func() { f.loop.Stop() })
	old := time.Now().Add(-time.Minute)
	_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})
	if _, err := f.loop.syncProjection(false); err != nil {
		t.Fatal(err)
	}
}

// delivery signs an intention to /root for a principal.
func (f *scopeFixture) delivery(principal, capability string, params map[string]any) junction.Delivery {
	t := f.t
	id := fmt.Sprintf("%08x-e89b-42d3-a456-426614174000", len(principal)*7919+len(capability)*104729+int(time.Now().UnixNano()%1000003))
	payload, _ := json.Marshal(map[string]any{
		"kind": circulation.ValueKindIntention,
		"intention": map[string]any{
			"intention_id": id, "await_response": true,
			"to":     map[string]any{"context": "/root", "cap": capability, "type": circulation.ValueTypeReflexive},
			"from":   map[string]any{"context": "/sender", "cap": "", "type": "user"},
			"params": params,
		},
	})
	protected := wire.Protected{
		Format: wire.Format, Version: wire.Version, Purpose: wire.PurposeIntention,
		AudienceInstanceID: "scope-instance", KeyID: f.keyIDs[principal],
		IssuedAtMS: "1792843200000", ExpiresAtMS: "1792843500000",
		IntentionID: id, ContentMode: wire.ContentModeClear,
		PayloadEncoding: "base64url", PayloadSize: strconv.Itoa(len(payload)), PayloadHash: wire.PayloadHash(payload),
	}
	protectedJSON, _ := json.Marshal(protected)
	input, err := wire.SigningInput(protectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(wire.Envelope{
		Protected: protectedJSON, Payload: base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.keys[principal], input)),
	})
	verified, err := wire.VerifyClear(envelope, "scope-instance", f.now, f.runtime.Active())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitExternalIntention(verified)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

// call runs a handler for an external principal exactly as the loop does after
// the door: the request is bound for the handler's lifetime.
func (f *scopeFixture) call(principal, capability string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	t := f.t
	delivery := f.delivery(principal, capability, params)
	message, _ := delivery.Message()
	if err := f.loop.authorizeExternalReflexive(delivery); err != nil {
		t.Fatalf("%s %s: the door refused: %v", principal, capability, err)
	}
	cleanup, err := f.loop.bindExternalRequest(delivery)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler(message)
	return f.response()
}

func (f *scopeFixture) response() circulation.Response {
	select {
	case d := <-f.comm:
		message, _ := d.Message()
		if reservation, ok := d.ExternalResponse(); ok {
			_ = reservation.Commit()
		}
		return message.Response
	case <-time.After(3 * time.Second):
		f.t.Fatal("no response")
		return circulation.Response{}
	}
}

// local runs a handler as a local caller (unrestricted).
func (f *scopeFixture) local(capability string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	handler(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "local-" + capability, Params: params,
		To:   circulation.Address{Context: "/root", Cap: capability, Type: circulation.ValueTypeReflexive},
		From: circulation.Address{Context: "/root", Type: circulation.ValueTypeUser},
	}})
	return f.response()
}

func scopeMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// allowed asks the source evaluator, never the code under test.
func (f *scopeFixture) allowed(principal, ctx, kind, name string) bool {
	if kind == "context" {
		name = ""
	}
	return f.src.Allowed(principal, "read", ctx, kind, name)
}

type queryRow struct{ ctx, kind, name string }

func rowsOf(t *testing.T, resp circulation.Response) []queryRow {
	t.Helper()
	payload := scopeMap(t, resp.Payload)
	var rows []queryRow
	list, _ := payload[circulation.KeyResult].([]any)
	for _, item := range list {
		m := item.(map[string]any)
		rows = append(rows, queryRow{m["ctx_id"].(string), m["element_type"].(string), m["name"].(string)})
	}
	return rows
}

func TestScopedMeaningQueryReturnsExactlyTheReadableRows(t *testing.T) {
	f := newScopeFixture(t)
	every := rowsOf(t, f.local("meaning.query", map[string]any{circulation.KeyLimit: 1000}, f.loop.capSQLiteMeaningQuery))
	seen := map[string]bool{}
	for _, row := range every {
		seen[row.name] = true
	}
	for _, name := range []string{"cap_root", "cap_secret", "cap_public", "doc_catalog", "sch_catalog"} {
		if !seen[name] {
			t.Fatalf("the projection lacks %s: %v", name, every)
		}
	}
	sort.Slice(every, func(i, j int) bool {
		a, b := every[i], every[j]
		if a.ctx != b.ctx {
			return a.ctx < b.ctx
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.name < b.name
	})
	for principal := range scopePrincipals {
		var want []queryRow
		for _, row := range every {
			if f.allowed(principal, row.ctx, row.kind, row.name) {
				want = append(want, row)
			}
		}
		got := rowsOf(t, f.callHandlerOnly(principal, "meaning.query", map[string]any{circulation.KeyLimit: 1000}, f.loop.capSQLiteMeaningQuery))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: rows\n got  %v\n want %v", principal, got, want)
		}
		// offset and limit count authorized rows only: pages concatenate to the whole.
		var paged []queryRow
		for offset := 0; offset < len(want)+2; offset += 2 {
			paged = append(paged, rowsOf(t, f.callHandlerOnly(principal, "meaning.query",
				map[string]any{circulation.KeyLimit: 2, circulation.KeyOffset: offset}, f.loop.capSQLiteMeaningQuery))...)
		}
		if fmt.Sprint(paged) != fmt.Sprint(want) {
			t.Errorf("%s: pagination\n got  %v\n want %v", principal, paged, want)
		}
		// Narrowing the request never widens the answer.
		narrowed := rowsOf(t, f.callHandlerOnly(principal, "meaning.query",
			map[string]any{circulation.KeyLimit: 1000, circulation.KeyElementKind: "capacity", circulation.KeyCtxId: "/root/catalog", circulation.KeyCtxMode: circulation.ValueCtxModeSubtree},
			f.loop.capSQLiteMeaningQuery))
		for _, row := range narrowed {
			if !f.allowed(principal, row.ctx, row.kind, row.name) || row.kind != "capacity" || !strings.HasPrefix(row.ctx, "/root/catalog") {
				t.Errorf("%s: narrowed query returned %v", principal, row)
			}
		}
	}
	// The local caller is not filtered.
	if again := rowsOf(t, f.local("meaning.query", map[string]any{circulation.KeyLimit: 1000}, f.loop.capSQLiteMeaningQuery)); len(again) != len(every) {
		t.Fatalf("a local caller was filtered: %d rows instead of %d", len(again), len(every))
	}
}

func TestScopedMeaningQueryCannotBeProbedWithFilters(t *testing.T) {
	f := newScopeFixture(t)
	// A filter naming content of an unreadable element returns nothing, exactly as
	// if the element did not exist.
	filters := []any{map[string]any{"path": "objective.name", "op": "EQ", "value": "cap_secret"}}
	for _, principal := range []string{"catalog", "single", "nobody", "named"} {
		rows := rowsOf(t, f.callHandlerOnly(principal, "meaning.query", map[string]any{circulation.KeyFilters: filters}, f.loop.capSQLiteMeaningQuery))
		if len(rows) != 0 {
			t.Errorf("%s probed an unreadable element: %v", principal, rows)
		}
	}
	if rows := rowsOf(t, f.callHandlerOnly("everything", "meaning.query", map[string]any{circulation.KeyFilters: filters}, f.loop.capSQLiteMeaningQuery)); len(rows) != 1 {
		t.Fatalf("the reader of everything should find it: %v", rows)
	}
}

// navPaths flattens a read.structure tree into path -> kind, and the names.
func navPaths(node map[string]any, out map[string]string, names map[string]bool) {
	path, _ := node["path"].(string)
	kind, _ := node["kind"].(string)
	out[path] = kind
	if name, _ := node["name"].(string); name != "" {
		names[name] = true
	}
	children, _ := node["children"].([]any)
	for _, child := range children {
		navPaths(child.(map[string]any), out, names)
	}
}

func (f *scopeFixture) expectedStructure(principal string) map[string]string {
	want := map[string]string{}
	rel := func(ctx string) string { return strings.TrimPrefix(strings.TrimPrefix(ctx, "/root"), "/") }
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, "/")
	}
	// The addressed root is always part of the tree (it is the result's root).
	want[""] = "context"
	for _, ctx := range scopeContexts {
		if !f.allowed(principal, ctx, "context", "") {
			continue
		}
		want[rel(ctx)] = "context"
		want[join(rel(ctx), "context.json")] = "context_file"
		byKind := map[string][]string{}
		for _, e := range scopeElements {
			if e.ctx == ctx && f.allowed(principal, ctx, e.kind, e.name) {
				byKind[e.kind] = append(byKind[e.kind], e.name)
			}
		}
		for kind, names := range byKind {
			want[join(rel(ctx), kind)] = "dir"
			item := kind
			switch kind {
			case "matter":
				item = circulation.ValueMatterItem
			case "structure":
				item = circulation.ValueStructureItem
			}
			for _, name := range names {
				suffix := ".json"
				if kind == "matter" {
					suffix = ""
				}
				if kind == "structure" {
					suffix = ""
				}
				want[join(rel(ctx), kind, name+suffix)] = item
			}
		}
	}
	return want
}

func TestScopedReadStructureShowsOnlyReadableContextsAndElements(t *testing.T) {
	f := newScopeFixture(t)
	params := map[string]any{configuration.KeyDepth: 8, configuration.KeyMaxPerPath: 100}
	full := f.local("read.structure", params, f.loop.capReadStructure)
	if status := full.Status; status != circulation.ValueStatusOK {
		t.Fatalf("local read.structure: %#v", full)
	}
	for principal := range scopePrincipals {
		if principal == "nobody" {
			continue // the door refuses it: no read_recursive. Handler-level behavior is checked below.
		}
		resp := f.callHandlerOnly(principal, "read.structure", params, f.loop.capReadStructure)
		root := scopeMap(t, resp.Payload)[circulation.KeyRoot].(map[string]any)
		got, names := map[string]string{}, map[string]bool{}
		navPaths(root, got, names)
		want := f.expectedStructure(principal)
		for path, kind := range want {
			if got[path] != kind {
				t.Errorf("%s: %q should be a %s, got %q", principal, path, kind, got[path])
			}
		}
		for path, kind := range got {
			if want[path] != kind {
				t.Errorf("%s: unexpected node %q (%s)", principal, path, kind)
			}
		}
		// Nothing unreadable is named anywhere in the response.
		text, _ := json.Marshal(resp.Payload)
		for _, e := range scopeElements {
			if !f.allowed(principal, e.ctx, e.kind, e.name) && strings.Contains(string(text), e.name) {
				t.Errorf("%s: response names the unreadable element %s", principal, e.name)
			}
		}
		for _, ctx := range scopeContexts[1:] {
			if !f.allowed(principal, ctx, "context", "") && names[filepath.Base(ctx)] {
				t.Errorf("%s: response names the unreadable context %s", principal, ctx)
			}
		}
		// Hidden implementation directories are not a resource genre.
		if strings.Contains(string(text), `"code"`) || strings.Contains(string(text), `"ui"`) {
			t.Errorf("%s: code/ui directories reached an external caller", principal)
		}
	}
}

// callHandlerOnly binds the request but skips the door (some principals are
// refused there by design; the filter must hold even if the door were wrong).
func (f *scopeFixture) callHandlerOnly(principal, capability string, params map[string]any, handler func(circulation.Message)) circulation.Response {
	delivery := f.delivery(principal, capability, params)
	message, _ := delivery.Message()
	cleanup, err := f.loop.bindExternalRequest(delivery)
	if err != nil {
		f.t.Fatal(err)
	}
	defer cleanup()
	handler(message)
	return f.response()
}

func TestScopedReadStructurePromotesReadableContextsUnderTheirFullPath(t *testing.T) {
	f := newScopeFixture(t)
	resp := f.callHandlerOnly("promoted", "read.structure", map[string]any{configuration.KeyDepth: 8}, f.loop.capReadStructure)
	root := scopeMap(t, resp.Payload)[circulation.KeyRoot].(map[string]any)
	got, names := map[string]string{}, map[string]bool{}
	navPaths(root, got, names)
	if got["catalog"] != "" {
		t.Fatal("the excepted context is in the tree")
	}
	if got["catalog/public"] != "context" || got["catalog/secret"] != "context" {
		t.Fatalf("readable contexts below an unreadable parent were dropped: %v", got)
	}
	if names["catalog"] {
		t.Fatal("the unreadable parent's name leaked")
	}
	children, _ := root["children"].([]any)
	foundAtRoot := 0
	for _, c := range children {
		if p := c.(map[string]any)["path"]; p == "catalog/public" || p == "catalog/secret" {
			foundAtRoot++
		}
	}
	if foundAtRoot != 2 {
		t.Fatalf("promoted contexts are not attached to the nearest readable ancestor: %v", children)
	}
}

func TestScopedReadStructureCountsAndTruncationFollowTheReadableSet(t *testing.T) {
	f := newScopeFixture(t)
	// Thirty unreadable matters sort before the one readable: the page must be
	// neither filled with them nor hidden behind them, and the counters describe
	// what the caller can see.
	resp := f.callHandlerOnly("page", "read.structure", map[string]any{configuration.KeyDepth: 4, configuration.KeyMaxPerPath: 3}, f.loop.capReadStructure)
	text, _ := json.Marshal(resp.Payload)
	if strings.Contains(string(text), "aaa_unreadable") {
		t.Fatal("unreadable matters appear in the page")
	}
	root := scopeMap(t, resp.Payload)[circulation.KeyRoot].(map[string]any)
	matterDir := findNode(root, "other/matter")
	if matterDir == nil {
		t.Fatalf("the matter directory is missing: %s", text)
	}
	if matterDir["children_total"] != float64(1) || matterDir["children_truncated"] == true {
		t.Fatalf("counters describe the unfiltered directory: %v", matterDir)
	}
	if !strings.Contains(string(text), "mat_other") {
		t.Fatal("the readable matter is hidden behind unreadable ones")
	}
}

func TestScopedReadStateIsReduced(t *testing.T) {
	f := newScopeFixture(t)
	local := scopeMap(t, f.local("read.state", map[string]any{}, f.loop.capReadState).Payload)
	if _, ok := local[circulation.KeyContext].(map[string]any)[circulation.KeyContextDir]; !ok {
		t.Fatal("the local state should keep the directory")
	}
	external := f.callHandlerOnly("everything", "read.state", map[string]any{}, f.loop.capReadState)
	text, _ := json.Marshal(external.Payload)
	if strings.Contains(string(text), f.root) || strings.Contains(string(text), circulation.KeyContextDir) {
		t.Fatalf("a file path reached an external caller: %s", text)
	}
	if _, ok := scopeMap(t, external.Payload)[circulation.KeyFamilies]; !ok {
		t.Fatal("the reduced state lost the families")
	}
}

func TestScopedTraceInspectWithholdsMessageContentWithoutThePayloadsRight(t *testing.T) {
	f := newScopeFixture(t)
	base := time.Now().UTC().Add(-2 * time.Second)
	writeTraceSegment(t, filepath.Join(f.root, traceDirName), base, []map[string]any{{
		circulation.KeyTS: base.Add(100 * time.Millisecond).Format(time.RFC3339Nano), circulation.KeyContextId: "/root",
		circulation.KeyTraceKind: circulation.ValueTraceFamilyEnter, circulation.KeyFamily: circulation.ValueOriginReflexive,
		circulation.KeyIntentionId: "i1", circulation.KeyUserText: "SECRET-USER-TEXT",
		circulation.KeyIntention: map[string]any{circulation.KeyIntentionID: "i1", "params": map[string]any{"password": "SECRET-PAYLOAD"}},
	}})
	params := func(payloads bool) map[string]any {
		return map[string]any{circulation.KeyMode: circulation.ValueModeEvents, circulation.KeyLimit: 10, circulation.KeyIncludePayloads: payloads}
	}
	// The reader of everything holds the payloads right (trace_payloads).
	with := f.call("everything", "trace.inspect", params(true), f.loop.capTraceInspect)
	if text, _ := json.Marshal(with.Payload); !strings.Contains(string(text), "SECRET-PAYLOAD") || !strings.Contains(string(text), "SECRET-USER-TEXT") {
		t.Fatalf("the holder of trace/events/payloads did not get the content: %s", text)
	}
	// The same reader, without asking for it, still gets the user text it is entitled to.
	// A reader without the right gets the structure of the trace and nothing derived from content.
	without := f.callHandlerOnly("catalog", "trace.inspect", params(false), f.loop.capTraceInspect)
	if text, _ := json.Marshal(without.Payload); strings.Contains(string(text), "SECRET") || strings.Contains(string(text), circulation.KeyUserText) {
		t.Fatalf("content leaked without the right: %s", text)
	}
	events, _ := scopeMap(t, without.Payload)[circulation.KeyEvents].([]any)
	if len(events) != 1 || events[0].(map[string]any)[circulation.KeyIntentionId] != "i1" {
		t.Fatalf("the trace itself should still be returned: %v", events)
	}
	// Explicitly asking for content without the right is refused by the door...
	delivery := f.delivery("catalog", "trace.inspect", params(true))
	if err := f.loop.authorizeExternalReflexive(delivery); err == nil {
		t.Fatal("the door let a request for message content through without the right")
	}
	// ...and by the handler, even if the door were bypassed.
	refused := f.callHandlerOnly("catalog", "trace.inspect", params(true), f.loop.capTraceInspect)
	if refused.Status == circulation.ValueStatusOK {
		t.Fatalf("the handler served message content without the right: %#v", refused)
	}
}

func TestVocabularyNeedsReadOnTheWholeInstance(t *testing.T) {
	f := newScopeFixture(t)
	for principal, want := range map[string]bool{
		"everything": true, "promoted": false /* excepts /root/catalog only: read_recursive on /root survives */, "catalog": false,
		"single": false, "named": false, "matters": false, "nobody": false, "rootonly": false,
	} {
		if principal == "promoted" {
			want = true // a T1 exception below the root does not remove read_recursive at /root
		}
		for _, capability := range []string{"vocabulary.get", "vocabulary.query"} {
			err := f.loop.authorizeExternalReflexive(f.delivery(principal, capability, map[string]any{}))
			if (err == nil) != want {
				t.Errorf("%s %s: door error = %v, want allowed=%v", principal, capability, err, want)
			}
		}
	}
	// Mutating the vocabulary stays closed to every external caller.
	for _, capability := range []string{"vocabulary.patch", "vocabulary.delete"} {
		if err := f.loop.authorizeExternalReflexive(f.delivery("everything", capability, map[string]any{})); err == nil {
			t.Errorf("%s reached an external caller", capability)
		}
	}
}

func findNode(node map[string]any, path string) map[string]any {
	if node["path"] == path {
		return node
	}
	children, _ := node["children"].([]any)
	for _, child := range children {
		if found := findNode(child.(map[string]any), path); found != nil {
			return found
		}
	}
	return nil
}

// The SQL pre-filter and the subtree skipping are optimizations (the exact check
// decides), so only their shape can pin them down.
func TestReadScopePrefilterAndSubtreeSkipping(t *testing.T) {
	f := newScopeFixture(t)
	scopeOf := func(principal string) *scopeView {
		delivery := f.delivery(principal, "read.structure", map[string]any{})
		admitted, _ := delivery.ExternalAdmission()
		return &scopeView{admitted.ReadScope(f.runtime)}
	}
	clause, args, nothing := intervalClause(scopeOf("catalog").ReadScope)
	if nothing || fmt.Sprint(args) != "[/root/catalog /root/catalog0]" || !strings.Contains(clause, "e.ctx_id >= ? AND e.ctx_id < ?") || strings.Contains(clause, " OR ") {
		t.Fatalf("subtree interval: %q %v %v", clause, args, nothing)
	}
	clause, args, nothing = intervalClause(scopeOf("single").ReadScope)
	if nothing || fmt.Sprint(args) != "[/root/catalog/public]" || strings.Contains(clause, ">=") || !strings.Contains(clause, "e.ctx_id = ?") {
		t.Fatalf("exact interval: %q %v %v", clause, args, nothing)
	}
	if clause, args, nothing = intervalClause(scopeOf("everything").ReadScope); nothing || clause != "" || args != nil {
		t.Fatalf("a subtree interval on the root needs no clause: %q %v", clause, args)
	}
	if _, _, nothing = intervalClause(scopeOf("nobody").ReadScope); !nothing {
		t.Fatal("a principal with no read right must produce an empty pre-filter")
	}
	single := scopeOf("single")
	for ctx, want := range map[string]bool{
		"/root": true, "/root/catalog": true, "/root/catalog/public": true,
		"/root/catalog/public/under": false, "/root/catalog/secret": false, "/root/hidden": false, "/root/other": false,
	} {
		if got := single.MayContainReadable(ctx); got != want {
			t.Errorf("MayContainReadable(%s) = %v, want %v", ctx, got, want)
		}
	}
	if !scopeOf("everything").MayContainReadable("/root/hidden/deep") || scopeOf("nobody").MayContainReadable("/root") {
		t.Fatal("subtree skipping does not follow the read rights")
	}
}

type scopeView struct{ *admission.ReadScope }

// BenchmarkQueryScoped compares a filtered meaning.query for an external caller
// with the unfiltered baseline of phase 0 on the same synthetic instance
// (BRIQUE_BENCH_ELEMENTS, default 10,000). "signing_only" is the cost of
// building the signed request, which every scoped case also pays.
func BenchmarkQueryScoped(b *testing.B) {
	q := buildQueryBaseline(b)
	f := &scopeFixture{t: &testing.T{}, now: time.UnixMilli(1792843200000).UTC(), keys: map[string]ed25519.PrivateKey{}, keyIDs: map[string]string{}}
	f.src = &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "scope-instance", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{}}
	for name, grants := range map[string][]policysrc.Grant{
		"one-context": {{Read: policysrc.TargetList{"/root/area03/team02"}}},
		"a-tenth":     {{Read: policysrc.TargetList{"/root/area03/**"}}},
		"everything":  {{Read: policysrc.TargetList{"/root/**"}}},
	} {
		public, private, _ := ed25519.GenerateKey(nil)
		keyID, _ := trust.SigningKeyID(public)
		f.keys[name], f.keyIDs[name] = private, keyID
		f.src.Principals[name] = &policysrc.Principal{Kind: "instance", Keys: []policysrc.Key{{PublicKey: base64.RawURLEncoding.EncodeToString(public)}}, Grants: grants}
	}
	f.activate()
	q.l.frame.CtxCommReg = f.registry
	q.l.handlerSlots = make(chan struct{}, 4)

	run := func(principal string, params map[string]any) int {
		delivery := f.delivery(principal, "meaning.query", params)
		message, _ := delivery.Message()
		cleanup, err := q.l.bindExternalRequest(delivery)
		if err != nil {
			b.Fatal(err)
		}
		defer cleanup()
		q.last = circulation.Message{}
		q.l.capSQLiteMeaningQuery(message)
		deadline := time.Now().Add(2 * time.Second)
		for q.last.Response.IntentionID != message.Intention.IntentionID && time.Now().Before(deadline) {
			time.Sleep(10 * time.Microsecond)
		}
		if q.last.Response.Status != circulation.ValueStatusOK {
			b.Fatalf("query failed: %#v", q.last.Response.Error)
		}
		rows, _ := q.last.Response.Payload[circulation.KeyResult].([]any)
		return len(rows)
	}
	b.Run("signing_only", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			f.delivery("everything", "meaning.query", map[string]any{})
		}
	})
	for _, c := range []struct {
		principal string
		params    map[string]any
	}{
		{"one-context", map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50}},
		{"a-tenth", map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50}},
		{"everything", map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50}},
		{"a-tenth", map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50, circulation.KeyOffset: 600}},
	} {
		c := c
		name := c.principal
		if c.params[circulation.KeyOffset] != nil {
			name += "_offset600"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportMetric(float64(run(c.principal, c.params)), "rows")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				run(c.principal, c.params)
			}
		})
	}
}

// rawCall sends a signed external intention through the real inbox (door and
// handler) and returns what Comm receives, whatever it is.
func (f *scopeFixture) rawCall(principal, capability string, params map[string]any) circulation.Response {
	delivery := f.delivery(principal, capability, params)
	f.loop.handleInboxDelivery(delivery)
	select {
	case d := <-f.comm:
		message, _ := d.Message()
		if reservation, ok := d.ExternalResponse(); ok {
			_ = reservation.Commit()
		}
		return message.Response
	case <-time.After(3 * time.Second):
		return circulation.Response{Status: "no-response"}
	}
}

// shapeOf is the content of an answer: everything but ids, times and signatures.
func shapeOf(r circulation.Response) string {
	out := map[string]any{"status": r.Status}
	if r.Error != nil {
		out["error"] = map[string]any{"origin": r.Error.Origin, "code": r.Error.Code, "message": r.Error.Message, "details": r.Error.Details}
	} else {
		out["payload"] = r.Payload
	}
	encoded, _ := json.Marshal(out)
	return string(encoded)
}
