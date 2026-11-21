package reflexive

import (
	"sort"
	"strings"
	"testing"
)

// The vocabulary only grows: folding duplicate roots must never lose a word. When two duplicate roots
// both carry a child of the same name, UNIQUE(parent_id, kind, seg) forbids moving the second one next
// to the first, so the subtrees have to be merged, not skipped and then deleted with their root.
func TestVocabularySchemaMergesCollidingChildrenOfDuplicateRoots(t *testing.T) {
	db := openVocabTestDB(t)
	if _, err := db.Exec(rawVocabNodesTable); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vocab_nodes(node_id, parent_id, kind, seg) VALUES
		(1, NULL, 'seg', 'functional'),
		(2, NULL, 'seg', 'functional'),
		(3, NULL, 'seg', 'functional'),
		(4, 2, 'seg', 'a'),
		(5, 3, 'seg', 'a'),
		(6, 4, 'seg', 'p'),
		(7, 5, 'seg', 'q'),
		(8, 3, 'seg', 'only_in_third')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := createVocabularySchema(db); err != nil {
		t.Fatalf("createVocabularySchema: %v", err)
	}

	words := func(query string) string {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var word string
			if err := rows.Scan(&word); err != nil {
				t.Fatal(err)
			}
			out = append(out, word)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := words(`SELECT seg FROM vocab_nodes WHERE parent_id IS NULL`); got != "functional" {
		t.Fatalf("roots = %q, want one functional root", got)
	}
	if got := words(`SELECT c.seg FROM vocab_nodes c JOIN vocab_nodes r ON c.parent_id=r.node_id WHERE r.parent_id IS NULL`); got != "a,only_in_third" {
		t.Fatalf("children of the root = %q, want a,only_in_third (the two 'a' merged, none lost)", got)
	}
	if got := words(`SELECT g.seg FROM vocab_nodes g JOIN vocab_nodes c ON g.parent_id=c.node_id WHERE c.seg='a'`); got != "p,q" {
		t.Fatalf("grandchildren under 'a' = %q, want p,q: a subtree was lost", got)
	}
	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vocab_nodes c WHERE c.parent_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM vocab_nodes p WHERE p.node_id = c.parent_id)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d orphaned vocabulary nodes", orphans)
	}
}

func TestVocabularyFoldMergesCollidingValuesAndIsIdempotent(t *testing.T) {
	db := openVocabTestDB(t)
	if _, err := db.Exec(rawVocabNodesTable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_vocab_val_text ON vocab_nodes(parent_id, v_text) WHERE kind='val' AND vtype='text'`); err != nil {
		t.Fatal(err)
	}
	// vocab_nodes has the value columns of the real schema only after createVocabularySchema, so the
	// raw table is extended the way the older databases already carry them.
	if _, err := db.Exec(`INSERT INTO vocab_nodes(node_id, parent_id, kind, seg) VALUES
		(1, NULL, 'seg', 'functional'), (2, NULL, 'seg', 'functional'), (3, 1, 'seg', 'k'), (4, 2, 'seg', 'k')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO vocab_nodes(node_id, parent_id, kind, vtype, v_text) VALUES
		(5, 3, 'val', 'text', 'v1'), (6, 4, 'val', 'text', 'v1'), (7, 4, 'val', 'text', 'v2')`); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ { // the second start must change nothing
		if err := createVocabularySchema(db); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		rows, err := db.Query(`SELECT v.v_text FROM vocab_nodes v JOIN vocab_nodes k ON v.parent_id = k.node_id WHERE k.seg='k' AND v.kind='val' ORDER BY v.v_text`)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		rows.Close()
		if strings.Join(values, ",") != "v1,v2" {
			t.Fatalf("pass %d: values under k = %v, want v1,v2 once each", pass, values)
		}
	}
}
