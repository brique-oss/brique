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

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newExecUtilitiesLoop(boundary map[string]shared.ContextAddr) *ExecutionLoop {
	return &ExecutionLoop{
		frame: &junction.ContextRegistry{
			CtxId:      "/ctx/a",
			CtxCommReg: &mockExecCommReg{boundary: boundary},
		},
		capCache: map[string]CapEntry{},
	}
}

func TestExecutionUtilities_N1_EXUT_01_GetBriqueSection(t *testing.T) {
	if got := getBriqueSection(nil); got != nil {
		t.Fatalf("nil doc should return nil")
	}
	if got := getBriqueSection(map[string]any{"x": 1}); got != nil {
		t.Fatalf("missing brique section should return nil")
	}
	syn := map[string]any{"a": "b"}
	if got := getBriqueSection(map[string]any{circulation.KeyBrique: syn}); got == nil || got["a"] != "b" {
		t.Fatalf("expected brique section map, got %#v", got)
	}
}

func TestExecutionUtilities_N1_EXUT_02_SynStr(t *testing.T) {
	if got := synStr(nil, "k"); got != "" {
		t.Fatalf("nil map should return empty string")
	}
	if got := synStr(map[string]any{"k": 42}, "k"); got != "" {
		t.Fatalf("non-string should return empty string")
	}
	if got := synStr(map[string]any{"k": "  val  "}, "k"); got != "val" {
		t.Fatalf("expected trimmed string, got %q", got)
	}
}

func TestExecutionUtilities_N1_EXUT_03_LoadCapEntryFromDisk(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	if _, ok := l.loadCapEntryFromDisk(""); ok {
		t.Fatalf("empty cap name should fail")
	}
	if _, ok := l.loadCapEntryFromDisk("cap"); ok {
		t.Fatalf("empty capRoot should fail")
	}

	dir := t.TempDir()
	l.capRoot = dir

	good := `{"brique":{"cap_name":"cap.good","rel_ctx":"/x","wrapper":"w1","lang":"go","kind":"compiled"}}`
	if err := os.WriteFile(filepath.Join(dir, "cap.good.json"), []byte(good), 0o644); err != nil {
		t.Fatalf("write good cap file: %v", err)
	}
	entry, ok := l.loadCapEntryFromDisk("cap.good")
	if !ok {
		t.Fatalf("expected successful load from disk")
	}
	if entry.CapName != "cap.good" || entry.Wrapper != "w1" || entry.Kind != "compiled" {
		t.Fatalf("unexpected loaded entry: %#v", entry)
	}

	mismatch := `{"brique":{"cap_name":"other.cap"}}`
	if err := os.WriteFile(filepath.Join(dir, "cap.mismatch.json"), []byte(mismatch), 0o644); err != nil {
		t.Fatalf("write mismatch cap file: %v", err)
	}
	if _, ok := l.loadCapEntryFromDisk("cap.mismatch"); ok {
		t.Fatalf("cap_name mismatch should fail")
	}

	if err := os.WriteFile(filepath.Join(dir, "cap.bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("write invalid json file: %v", err)
	}
	if _, ok := l.loadCapEntryFromDisk("cap.bad"); ok {
		t.Fatalf("invalid json should fail")
	}
}

func TestExecutionUtilities_N1_EXUT_04_GetCapEntryCacheAndLazyLoad(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	l.capCache["cap.cached"] = CapEntry{CapName: "cap.cached", Kind: "dsl"}

	ce, ok := l.getCapEntry("cap.cached")
	if !ok || ce.CapName != "cap.cached" {
		t.Fatalf("expected cache hit, got ce=%#v ok=%v", ce, ok)
	}

	dir := t.TempDir()
	l.capRoot = dir
	payload := `{"brique":{"cap_name":"cap.disk","wrapper":"w1","kind":"interpreted"}}`
	if err := os.WriteFile(filepath.Join(dir, "cap.disk.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write disk cap file: %v", err)
	}

	ce, ok = l.getCapEntry("cap.disk")
	if !ok || ce.CapName != "cap.disk" || ce.Kind != "interpreted" {
		t.Fatalf("expected lazy disk load, got ce=%#v ok=%v", ce, ok)
	}
	if _, exists := l.capCache["cap.disk"]; !exists {
		t.Fatalf("expected lazy-loaded entry cached")
	}

	if _, ok := l.getCapEntry(" "); ok {
		t.Fatalf("blank cap should fail")
	}
}

func TestExecutionUtilities_N1_EXUT_05_RewriteResponseToWrapperTransport(t *testing.T) {
	l := newExecUtilitiesLoop(map[string]shared.ContextAddr{"w1": "/ctx/a/w1"})

	resp := &circulation.Response{To: circulation.Address{Context: "/ctx/a/w1/sub/path"}}
	l.rewriteResponseToWrapperTransport(resp, "w1")
	if string(resp.To.Context) != "@wrapper_w1:/sub/path" {
		t.Fatalf("unexpected rewritten context: %q", resp.To.Context)
	}

	respEq := &circulation.Response{To: circulation.Address{Context: "/ctx/a/w1"}}
	l.rewriteResponseToWrapperTransport(respEq, "w1")
	if string(respEq.To.Context) != "@wrapper_w1:/" {
		t.Fatalf("boundary-equal context rewrite unexpected: %q", respEq.To.Context)
	}

	respFallback := &circulation.Response{To: circulation.Address{Context: "/other/domain/x"}}
	l.rewriteResponseToWrapperTransport(respFallback, "w1")
	if string(respFallback.To.Context) != "@wrapper_w1:/other/domain/x" {
		t.Fatalf("fallback rewrite unexpected: %q", respFallback.To.Context)
	}

	already := &circulation.Response{To: circulation.Address{Context: "@wrapper_w1:/already"}}
	l.rewriteResponseToWrapperTransport(already, "w1")
	if string(already.To.Context) != "@wrapper_w1:/already" {
		t.Fatalf("already wrapper transport should stay unchanged")
	}

	respNoBoundary := &circulation.Response{To: circulation.Address{Context: "/ctx/a/w1/sub"}}
	l2 := newExecUtilitiesLoop(map[string]shared.ContextAddr{})
	l2.rewriteResponseToWrapperTransport(respNoBoundary, "w1")
	if string(respNoBoundary.To.Context) != "/ctx/a/w1/sub" {
		t.Fatalf("missing boundary should keep original context")
	}
}

func TestExecutionUtilities_N1_EXUT_06_RewriteToWrapperTransport(t *testing.T) {
	l := newExecUtilitiesLoop(map[string]shared.ContextAddr{"w1": "/ctx/a/w1"})

	msg := &circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: circulation.Intention{To: circulation.Address{Context: "/ctx/a/w1/sub/path"}},
	}
	l.rewriteToWrapperTransport(msg, "w1")
	if string(msg.Intention.To.Context) != "@wrapper_w1:/sub/path" {
		t.Fatalf("unexpected rewritten context: %q", msg.Intention.To.Context)
	}

	msgEq := &circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "/ctx/a/w1"}}}
	l.rewriteToWrapperTransport(msgEq, "w1")
	if string(msgEq.Intention.To.Context) != "@wrapper_w1:/" {
		t.Fatalf("boundary-equal rewrite unexpected: %q", msgEq.Intention.To.Context)
	}

	msgFallback := &circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "/x/y"}}}
	l.rewriteToWrapperTransport(msgFallback, "w1")
	if string(msgFallback.Intention.To.Context) != "@wrapper_w1:/x/y" {
		t.Fatalf("fallback rewrite unexpected: %q", msgFallback.Intention.To.Context)
	}

	already := &circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "@wrapper_w1:/already"}}}
	l.rewriteToWrapperTransport(already, "w1")
	if string(already.Intention.To.Context) != "@wrapper_w1:/already" {
		t.Fatalf("already wrapper transport should stay unchanged")
	}

	notIntention := &circulation.Message{Kind: circulation.ValueKindResponse}
	l.rewriteToWrapperTransport(notIntention, "w1")
	if notIntention.Kind != circulation.ValueKindResponse {
		t.Fatalf("non-intention message should remain unchanged")
	}

	msgNoBoundary := &circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{To: circulation.Address{Context: "/ctx/a/w1/sub"}}}
	l2 := newExecUtilitiesLoop(map[string]shared.ContextAddr{})
	l2.rewriteToWrapperTransport(msgNoBoundary, "w1")
	if string(msgNoBoundary.Intention.To.Context) != "/ctx/a/w1/sub" {
		t.Fatalf("missing boundary should keep original context")
	}
}

func TestExecutionUtilities_N1_EXUT_07_LoadCapEntryUsesConfigKeys(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir

	doc := map[string]any{
		circulation.KeyBrique: map[string]any{
			configuration.KeyCapName:   "cap.keyed",
			configuration.KeyCapRelCtx: "/rel",
			configuration.KeyCapWrp:    "w2",
			configuration.KeyCapLang:   "py",
			configuration.KeyCapKind:   "interpreted",
		},
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "cap.keyed.json"), b, 0o644); err != nil {
		t.Fatalf("write keyed file: %v", err)
	}
	ce, ok := l.loadCapEntryFromDisk("cap.keyed")
	if !ok {
		t.Fatalf("expected keyed load success")
	}
	if ce.RelCtx != "/rel" || ce.Wrapper != "w2" || ce.Lang != "py" || ce.Kind != "interpreted" {
		t.Fatalf("unexpected keyed entry: %#v", ce)
	}
}
