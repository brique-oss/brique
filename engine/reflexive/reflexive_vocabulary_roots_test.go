package reflexive

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
)

// rawVocabNodesTable is the vocab_nodes table as created before the root index
// existed: the only constraint is UNIQUE(parent_id, kind, seg), which does not
// deduplicate rows whose parent_id is NULL.
const rawVocabNodesTable = `CREATE TABLE vocab_nodes (
	node_id   INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES vocab_nodes(node_id) ON DELETE CASCADE,
	kind      TEXT NOT NULL CHECK(kind IN ('seg','val')),
	seg       TEXT,
	vtype     TEXT CHECK(vtype IN ('text','num')),
	v_text    TEXT,
	v_num     REAL,
	CHECK(
		(kind='seg' AND seg IS NOT NULL AND vtype IS NULL AND v_text IS NULL AND v_num IS NULL)
	 OR (kind='val' AND seg IS NULL)
	),
	UNIQUE(parent_id, kind, seg)
);`

func openVocabTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openSQLite(filepath.Join(t.TempDir(), "vocab.sqlite"))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVocabularyRootsAreNotDuplicated(t *testing.T) {
	db := openVocabTestDB(t)
	if err := createVocabularySchema(db); err != nil {
		t.Fatalf("createVocabularySchema: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := vocabEnsureSegPath(db, "functional.a.b"); err != nil {
			t.Fatalf("vocabEnsureSegPath: %v", err)
		}
	}
	var roots int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vocab_nodes WHERE parent_id IS NULL`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if roots != 1 {
		t.Fatalf("root rows = %d, want 1", roots)
	}
}

func TestVocabularySchemaFoldsExistingDuplicateRoots(t *testing.T) {
	db := openVocabTestDB(t)
	if _, err := db.Exec(rawVocabNodesTable); err != nil {
		t.Fatalf("create table: %v", err)
	}
	// three duplicate "functional" roots; the 2nd and 3rd carry one child each
	if _, err := db.Exec(`INSERT INTO vocab_nodes(node_id, parent_id, kind, seg) VALUES
		(1, NULL, 'seg', 'functional'),
		(2, NULL, 'seg', 'functional'),
		(3, NULL, 'seg', 'functional'),
		(4, 2, 'seg', 'x'),
		(5, 3, 'seg', 'y')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := createVocabularySchema(db); err != nil {
		t.Fatalf("createVocabularySchema: %v", err)
	}
	var roots int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vocab_nodes WHERE parent_id IS NULL AND seg='functional'`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if roots != 1 {
		t.Fatalf("functional roots = %d, want 1", roots)
	}
	rows, err := db.Query(`SELECT c.seg FROM vocab_nodes c JOIN vocab_nodes r ON c.parent_id = r.node_id
		WHERE r.parent_id IS NULL AND r.seg='functional'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		kids = append(kids, s)
	}
	sort.Strings(kids)
	if len(kids) != 2 || kids[0] != "x" || kids[1] != "y" {
		t.Fatalf("children of the surviving root = %v, want [x y]", kids)
	}
	// a second call is a no-op and the index now refuses new duplicates
	if err := createVocabularySchema(db); err != nil {
		t.Fatalf("second createVocabularySchema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vocab_nodes(parent_id, kind, seg) VALUES (NULL, 'seg', 'functional')`); err == nil {
		t.Fatal("inserting a duplicate root must fail once the index exists")
	}
}
