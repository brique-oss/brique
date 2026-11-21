package reflexive

import (
	"fmt"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

// A huge params.limit is clamped: one request cannot ask for the whole index.
func TestMeaningQueryClampsLimit(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	db := setupProjectionDB(t, ctxDir)
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxMeaningQueryLimit+100; i++ {
		name := fmt.Sprintf("cap%05d", i)
		if _, err := tx.Exec(`INSERT INTO elements(element_id, element_type, name, ctx_id) VALUES(?,?,?,?)`,
			"ctx:/root:capacity:"+name, circulation.ValueCapacity, name, shared.RootContextID); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	l.capSQLiteMeaningQuery(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "clamp-1",
		To:          circulation.Address{Context: shared.RootContextID, Cap: "meaning.query", Type: circulation.ValueTypeReflexive},
		From:        circulation.Address{Context: shared.RootContextID},
		Params:      map[string]any{circulation.KeyLimit: 5000000},
	}})
	resp := recvReflexiveSQLiteMsg(t, commCh, "clamped query").Response
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q error=%#v", resp.Status, resp.Error)
	}
	n := 0
	switch rows := resp.Payload[circulation.KeyResult].(type) {
	case []map[string]any:
		n = len(rows)
	case []any:
		n = len(rows)
	}
	if n != maxMeaningQueryLimit {
		t.Fatalf("rows = %d, want the clamp %d", n, maxMeaningQueryLimit)
	}
}
