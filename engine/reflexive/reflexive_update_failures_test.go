package reflexive

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

func updateTestCall(t *testing.T, l *ReflexiveLoop, commCh <-chan circulation.Message, id, capName string, params map[string]any) circulation.Response {
	t.Helper()
	msg := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: shared.RootContextID, Cap: capName, Type: circulation.ValueTypeReflexive},
		From:        circulation.Address{Context: shared.RootContextID},
		Params:      params,
	}}
	switch capName {
	case "meaning.update":
		l.capSQLiteUpdate(msg)
	case "meaning.query":
		l.capSQLiteMeaningQuery(msg)
	default:
		t.Fatalf("unexpected capacity %q", capName)
	}
	return recvReflexiveSQLiteMsg(t, commCh, id).Response
}

func updateRef(name string) map[string]any {
	return map[string]any{circulation.KeyCtxId: shared.RootContextID, circulation.KeyElementKind: "capacity", circulation.KeyName: name}
}

// meaning.update reports the elements it could not index instead of hiding them
// behind a success, and the elements it did index stay searchable.
func TestMeaningUpdateReportsFailedElements(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	writeContextDescriptor(t, ctxDir)
	db := setupProjectionDB(t, ctxDir)
	_ = db.Close()
	capDir := filepath.Join(ctxDir, "capacity")
	if err := os.MkdirAll(capDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(capDir, name+".json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good", capDoc("good", "first"))
	write("broken", `{"brique": not json`)

	resp := updateTestCall(t, l, commCh, "u-1", "meaning.update", map[string]any{
		circulation.KeyElements: []any{updateRef("good"), updateRef("broken"), updateRef("absent")},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q error=%#v", resp.Status, resp.Error)
	}
	if got := resp.Payload["updated_elements_count"]; got != 1 {
		t.Fatalf("updated_elements_count=%v, want 1", got)
	}
	if got := resp.Payload["failed_count"]; got != 2 {
		t.Fatalf("failed_count=%v, want 2 (payload %#v)", got, resp.Payload)
	}
	failed, _ := resp.Payload["failed_elements"].([]map[string]any)
	names := map[any]bool{}
	for _, f := range failed {
		names[f[circulation.KeyName]] = true
	}
	if len(failed) != 2 || !names["broken"] || !names["absent"] {
		t.Fatalf("failed_elements=%#v, want broken and absent", failed)
	}

	q := updateTestCall(t, l, commCh, "q-1", "meaning.query", map[string]any{
		circulation.KeyElementKind: "capacity",
		circulation.KeyFilters: []any{map[string]any{
			circulation.KeyPath: "objective.description", circulation.KeyOp: "eq", circulation.KeyValue: "first"}},
	})
	if q.Status != circulation.ValueStatusOK {
		t.Fatalf("query status=%q error=%#v", q.Status, q.Error)
	}
	rows, _ := q.Payload[circulation.KeyResult].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "good" {
		t.Fatalf("query rows=%#v, want the indexed element", rows)
	}
}
