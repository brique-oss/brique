package reflexive

import (
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

func vocabCacheCall(t *testing.T, l *ReflexiveLoop, ch <-chan circulation.Message, id string, call func(circulation.Message), params map[string]any) circulation.Response {
	t.Helper()
	call(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: id, Params: params}})
	return recvReflexiveSQLiteMsg(t, ch, id).Response
}

func vocabCacheWords(resp circulation.Response, axis string) string {
	v, _ := resp.Payload[circulation.KeyVocabulary].(map[string]any)
	return strings.Join(flattenWords(v), ",")
}

func flattenWords(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			out = append(out, k)
			out = append(out, flattenWords(c)...)
		}
	case []any:
		for _, c := range x {
			out = append(out, flattenWords(c)...)
		}
	case string:
		out = append(out, x)
	}
	return out
}

// The cache must never serve a vocabulary older than the file: each change made
// by vocabulary.patch is visible to the next vocabulary.query and vocabulary.get.
func TestVocabularyCacheFollowsTheFile(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	setupProjectionDB(t, ctxDir).Close()

	patch := func(id string, words ...any) {
		t.Helper()
		fn := map[string]any{"status": words}
		if id == "c-patch-2" {
			fn["priority"] = []any{"high"} // a new key path: it shows in the LLM text map
		}
		resp := vocabCacheCall(t, l, commCh, id, l.capSQLiteVocabularyPatch, map[string]any{
			circulation.KeyPatch: map[string]any{"functional": fn},
		})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("%s: status=%q error=%#v", id, resp.Status, resp.Error)
		}
	}
	query := func(id string) circulation.Response {
		t.Helper()
		resp := vocabCacheCall(t, l, commCh, id, l.capSQLiteVocabularyQuery, map[string]any{circulation.KeyAxis: circulation.KeyFunctional})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("%s: status=%q error=%#v", id, resp.Status, resp.Error)
		}
		return resp
	}

	patch("c-patch-1", "alpha")
	if got := vocabCacheWords(query("c-query-1"), "functional"); !strings.Contains(got, "alpha") || strings.Contains(got, "beta") {
		t.Fatalf("first read: %q", got)
	}
	// a second read is served from the cache and is identical
	if got := vocabCacheWords(query("c-query-1b"), "functional"); !strings.Contains(got, "alpha") {
		t.Fatalf("cached read: %q", got)
	}
	patch("c-patch-2", "beta")
	// (a patch of a leaf list replaces the list: what matters is that beta is seen)
	if got := vocabCacheWords(query("c-query-2"), "functional"); !strings.Contains(got, "beta") {
		t.Fatalf("read after the second patch must see beta: %q", got)
	}

	get := vocabCacheCall(t, l, commCh, "c-get", l.capSQLiteVocabularyGet, nil)
	if get.Status != circulation.ValueStatusOK || !strings.Contains(vocabCacheWords(get, "functional"), "beta") {
		t.Fatalf("vocabulary.get after the patches: %#v", get)
	}
	llm := vocabCacheCall(t, l, commCh, "c-get-llm", l.capSQLiteVocabularyGet, map[string]any{circulation.KeyFile: "llm"})
	if content, _ := llm.Payload[circulation.KeyContent].(string); !strings.Contains(content, "priority") {
		t.Fatalf("the LLM map must follow the file too: %q", content)
	}
}
