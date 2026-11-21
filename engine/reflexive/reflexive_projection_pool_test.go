package reflexive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/engine/shared"
)

func poolPath(t *testing.T, l *ReflexiveLoop) string {
	t.Helper()
	db, err := l.projection()
	if err != nil {
		t.Fatalf("projection(): %v", err)
	}
	var seq int
	var name, file string
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&seq, &name, &file); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			return file
		}
	}
	t.Fatal("no main database")
	return ""
}

func TestProjectionPoolIsShared(t *testing.T) {
	l, _, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	if err := ensureProjectionDir(ctxDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.closeProjection)
	a, err := l.projection()
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.projection()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two calls returned two different pools")
	}
	l.closeProjection()
	c, err := l.projection()
	if err != nil || c == a {
		t.Fatalf("after closeProjection a new pool is expected (err=%v)", err)
	}
}

// Reading before any projection exists must still fail to open, as before.
func TestProjectionPoolDoesNotCreateTheDirectory(t *testing.T) {
	l, _, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	if _, err := l.projection(); err == nil {
		t.Fatal("projection() must fail when there is no projection directory")
	}
	if existsT(filepath.Join(ctxDir, projectionDirName)) {
		t.Fatal("projection() created the projection directory")
	}
}

func TestProjectionPoolReopensAfterFileRemoval(t *testing.T) {
	l, _, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	if err := ensureProjectionDir(ctxDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.closeProjection)
	db, err := l.projection()
	if err != nil {
		t.Fatal(err)
	}
	if err := createMeaningSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO elements(element_id, element_type, name, ctx_id) VALUES('x','capacity','x','/root')`); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dbPath(ctxDir)); err != nil { // the main file only, as an external cleanup would
		t.Fatal(err)
	}
	db2, err := l.projection()
	if err != nil {
		t.Fatalf("projection() after the file was removed: %v", err)
	}
	if db2 == db {
		t.Fatal("the pool must be reopened when its file is gone")
	}
	if err := createMeaningSchema(db2); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM elements`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the new database should be empty, got %d rows (err=%v)", n, err)
	}
}

func TestProjectionPoolFollowsGeneration(t *testing.T) {
	shortenOldGenerationGrace(t)
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	writeContextDescriptor(t, ctxDir)
	writeFileT(t, filepath.Join(ctxDir, "capacity", "alpha.json"), capDoc("alpha", "one"))
	setupProjectionDB(t, ctxDir).Close()
	t.Cleanup(l.closeProjection)

	before := poolPath(t, l)
	if filepath.Base(before) != "meaning.sqlite" {
		t.Fatalf("pool on %q, want the historical file", before)
	}
	rebuildFull(t, l, commCh, "pg-1")
	after := poolPath(t, l)
	if filepath.Base(after) != "meaning.1.sqlite" {
		t.Fatalf("after a rebuild the pool is on %q, want meaning.1.sqlite", after)
	}
	waitUntil(t, 3*time.Second, func() bool { return !existsT(before) }, "the replaced generation should be removed after the grace period")
}
