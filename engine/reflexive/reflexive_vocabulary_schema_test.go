package reflexive

import (
	"testing"

	"brique_engine/circulation"
)

func TestVocabularyPathForElement(t *testing.T) {
	for _, c := range []struct{ typ, in, want string }{
		// a schema's gabarit qualifies the axes of the elements it describes
		{"schema", "functional.objective.designation", "objective.designation"},
		{"schema", "functional.subjective.ressenti", "subjective.ressenti"},
		{"schema", "functional.brique.kind", "brique.kind"},
		{"schema", "functional.functional.status", "functional.status"},
		{"schema", "functional.objective", "objective"},
		// a schema for arbitrary data keeps its shape under functional
		{"schema", "functional.items", "functional.items"},
		{"schema", "functional.cursors.cardinality", "functional.cursors.cardinality"},
		// a schema's own words are not moved
		{"schema", "objective.description", "objective.description"},
		{"schema", "brique.kind", "brique.kind"},
		// other elements are never moved, even with the same shape
		{"capacity", "functional.objective.zzz", "functional.objective.zzz"},
		{"matter", "functional.subjective.x", "functional.subjective.x"},
		{"context", "functional.brique.x", "functional.brique.x"},
	} {
		if got := vocabularyPathForElement(c.typ, c.in); got != c.want {
			t.Errorf("%s %q -> %q, want %q", c.typ, c.in, got, c.want)
		}
	}
}

// End to end: the gabarit of a schema enters the vocabulary under the axes it
// describes, other elements are untouched, and the complete index keeps the real paths.
func TestRebuildPlacesSchemaGabaritUnderItsAxes(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, "/root")
	writeContextDescriptor(t, ctxDir)
	writeFileT(t, ctxDir+"/schema/plan.json", `{"brique":{"kind":"schema"},"objective":{"name":"plan","description":"d"},`+
		`"functional":{"brique":{},"objective":{"designation":[],"materiau":["acier","alu"]},"functional":{},"subjective":{}},"subjective":{}}`)
	writeFileT(t, ctxDir+"/schema/forme.json", `{"brique":{"kind":"schema"},"objective":{"name":"forme","description":"d"},`+
		`"functional":{"items":["un"]},"subjective":{}}`)
	writeFileT(t, ctxDir+"/capacity/cap.json", `{"brique":{"cap_name":"cap"},"objective":{"name":"cap","description":"d"},`+
		`"functional":{"objective":{"zzz":["valeur"]}},"subjective":{}}`)

	rebuildFull(t, l, commCh, "sv-1")
	vocab, err := loadVocabulary(ctxDir)
	if err != nil {
		t.Fatal(err)
	}
	has := func(path ...string) bool {
		var cur any = vocab
		for _, seg := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return false
			}
			if cur, ok = m[seg]; !ok {
				return false
			}
		}
		return true
	}
	if !has(circulation.KeyObjective, "designation") || !has(circulation.KeyObjective, "materiau") {
		t.Fatalf("the gabarit words must enter objective.*: %#v", vocab[circulation.KeyObjective])
	}
	if obj, _ := vocab[circulation.KeyObjective].(map[string]any); obj != nil {
		if vals, _ := obj["materiau"].([]any); len(vals) != 2 {
			t.Fatalf("the admissible values of objective.materiau should follow: %#v", obj["materiau"])
		}
	}
	if has(circulation.KeyFunctional, circulation.KeyObjective, "designation") {
		t.Fatalf("a schema's gabarit must not stay under functional.objective: %#v", vocab[circulation.KeyFunctional])
	}
	if !has(circulation.KeyFunctional, "items") {
		t.Fatalf("a data-shape schema keeps its words under functional: %#v", vocab[circulation.KeyFunctional])
	}
	if !has(circulation.KeyFunctional, circulation.KeyObjective, "zzz") {
		t.Fatalf("a capacity's functional.objective.zzz must not be moved: %#v", vocab[circulation.KeyFunctional])
	}

	// the complete index still has the real paths of the schema
	q := updateTestCall(t, l, commCh, "sv-q", "meaning.query", map[string]any{
		circulation.KeyElementKind: "schema",
		circulation.KeyFilters:     []any{map[string]any{circulation.KeyPath: "functional.objective.materiau", circulation.KeyOp: "eq", circulation.KeyValue: "acier"}},
	})
	rows, _ := q.Payload[circulation.KeyResult].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "plan" {
		t.Fatalf("the index must keep functional.objective.materiau for the schema: %#v", q.Payload)
	}
}
