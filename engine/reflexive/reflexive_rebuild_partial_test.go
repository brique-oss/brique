package reflexive_test

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

func countAllIndexedElements(t *testing.T, h *engineN6Harness, id string) int {
	t.Helper()
	return len(mustMeaningResultRows(t, queryN6Meaning(t, h, id, map[string]any{
		circulation.KeyLimit: 1000,
	})))
}

// A vocabulary-only rebuild must leave the meaning index as it was.
func TestRebuildVocabularyOnlyKeepsMeaningIndex(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "rbp-full")
	before := countAllIndexedElements(t, h, "rbp-count-before")
	if before == 0 {
		t.Fatal("the full rebuild indexed nothing: the test proves nothing")
	}

	resp := callN6Reflexive(t, h, shared.RootContextID, "rbp-vocab-only", "meaning.rebuild", map[string]any{
		circulation.KeyMode: circulation.ValueVocabOnly,
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("vocabulary-only rebuild status=%q error=%#v", resp.Status, resp.Error)
	}
	after := countAllIndexedElements(t, h, "rbp-count-after")
	if after != before {
		t.Fatalf("meaning index holds %d elements after a vocabulary-only rebuild, %d before", after, before)
	}
}

// A rebuild restricted to some contexts must not drop the other contexts.
func TestRebuildWithContextFilterKeepsOtherContexts(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "rbp2-full")
	before := countAllIndexedElements(t, h, "rbp2-count-before")

	resp := callN6Reflexive(t, h, shared.RootContextID, "rbp2-one-context", "meaning.rebuild", map[string]any{
		circulation.KeyMode:    circulation.ValueFull,
		circulation.KeyContext: []any{n6WorkspaceID},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("context-filtered rebuild status=%q error=%#v", resp.Status, resp.Error)
	}
	after := countAllIndexedElements(t, h, "rbp2-count-after")
	if after != before {
		t.Fatalf("meaning index holds %d elements after a context-filtered rebuild, %d before", after, before)
	}
}

// The listed context is refreshed: an element that disappeared from it leaves the
// index, the other contexts stay as they were.
func TestRebuildWithContextFilterRefreshesTheListedContext(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "rbp3-full")
	before := countAllIndexedElements(t, h, "rbp3-count-before")

	gone := filepath.Join(h.rootDir, "workspace", "capacity", "sample.echo.json")
	if err := os.Remove(gone); err != nil {
		t.Fatalf("the sandbox no longer has %s: %v", gone, err)
	}
	resp := callN6Reflexive(t, h, shared.RootContextID, "rbp3-one-context", "meaning.rebuild", map[string]any{
		circulation.KeyMode:    circulation.ValueFull,
		circulation.KeyContext: []any{n6WorkspaceID},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("context-filtered rebuild status=%q error=%#v", resp.Status, resp.Error)
	}
	if after := countAllIndexedElements(t, h, "rbp3-count-after"); after != before-1 {
		t.Fatalf("index holds %d elements, want %d (one element removed, nothing else lost)", after, before-1)
	}
	rows := mustMeaningResultRows(t, queryN6Meaning(t, h, "rbp3-caps", map[string]any{
		circulation.KeyCtxId: n6WorkspaceID, circulation.KeyElementKind: "capacity", circulation.KeyLimit: 100,
	}))
	if findMeaningQueryRow(rows, n6WorkspaceID, "capacity", "sample.echo") != nil {
		t.Fatal("the removed capacity is still indexed")
	}
	if findMeaningQueryRow(rows, n6WorkspaceID, "capacity", "sample.normalize") == nil {
		t.Fatal("a capacity that was not removed is missing")
	}
}
