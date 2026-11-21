package reflexive

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func existsT(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDbPathFollowsPointer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, projectionDirName)
	if got, want := dbPath(root), filepath.Join(dir, "meaning.sqlite"); got != want {
		t.Fatalf("without a pointer: %q, want %q", got, want)
	}
	writeFileT(t, filepath.Join(dir, meaningCurrentPointer), "meaning.3.sqlite\n")
	if got, want := dbPath(root), filepath.Join(dir, "meaning.3.sqlite"); got != want {
		t.Fatalf("with a pointer: %q, want %q", got, want)
	}
	if got, want := nextDBPath(root), filepath.Join(dir, "meaning.4.sqlite"); got != want {
		t.Fatalf("next generation: %q, want %q", got, want)
	}
}

func TestDbPathIgnoresMalformedPointer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, projectionDirName)
	legacy := filepath.Join(dir, "meaning.sqlite")
	for _, content := range []string{"../evil.sqlite", "meaning.sqlite/../../x", "meaning..sqlite", "", "\n", "/etc/passwd", "meaning.1234567890.sqlite"} {
		writeFileT(t, filepath.Join(dir, meaningCurrentPointer), content)
		if got := dbPath(root); got != legacy {
			t.Fatalf("pointer %q: dbPath = %q, want the historical name", content, got)
		}
	}
}

func rebuildFull(t *testing.T, l *ReflexiveLoop, ch <-chan circulation.Message, id string) {
	t.Helper()
	l.capSQLiteRebuild(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: shared.RootContextID, Cap: "meaning.rebuild", Type: circulation.ValueTypeReflexive},
		From:        circulation.Address{Context: shared.RootContextID},
		Params:      map[string]any{circulation.KeyMode: circulation.ValueFull},
	}})
	if resp := recvReflexiveSQLiteMsg(t, ch, id).Response; resp.Status != circulation.ValueStatusOK {
		t.Fatalf("%s: status=%q error=%#v", id, resp.Status, resp.Error)
	}
}

func TestRebuildPublishesNewGeneration(t *testing.T) {
	shortenOldGenerationGrace(t)
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	writeContextDescriptor(t, ctxDir)
	writeFileT(t, filepath.Join(ctxDir, "capacity", "alpha.json"), capDoc("alpha", "one"))
	dir := filepath.Join(ctxDir, projectionDirName)
	setupProjectionDB(t, ctxDir).Close() // the historical file, generation 0
	if !existsT(filepath.Join(dir, "meaning.sqlite")) {
		t.Fatal("the generation 0 file should exist")
	}

	rebuildFull(t, l, commCh, "g-1")
	pointer, err := os.ReadFile(filepath.Join(dir, meaningCurrentPointer))
	if err != nil || string(pointer) != "meaning.1.sqlite\n" {
		t.Fatalf("pointer = %q err=%v, want meaning.1.sqlite", pointer, err)
	}
	if !existsT(filepath.Join(dir, "meaning.1.sqlite")) {
		t.Fatal("meaning.1.sqlite should exist")
	}
	waitUntil(t, 3*time.Second, func() bool { return !existsT(filepath.Join(dir, "meaning.sqlite")) },
		"the generation 0 file should be gone after the first rebuild")
	q := updateTestCall(t, l, commCh, "g-q1", "meaning.query", map[string]any{
		circulation.KeyElementKind: "capacity", circulation.KeyLimit: 10})
	rows, _ := q.Payload[circulation.KeyResult].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "alpha" {
		t.Fatalf("query after the first rebuild = %#v", q.Payload)
	}

	rebuildFull(t, l, commCh, "g-2")
	pointer, _ = os.ReadFile(filepath.Join(dir, meaningCurrentPointer))
	if string(pointer) != "meaning.2.sqlite\n" {
		t.Fatalf("pointer = %q, want meaning.2.sqlite", pointer)
	}
	waitUntil(t, 3*time.Second, func() bool { return !existsT(filepath.Join(dir, "meaning.1.sqlite")) },
		"meaning.1.sqlite should be gone after the second rebuild")
	q = updateTestCall(t, l, commCh, "g-q2", "meaning.query", map[string]any{
		circulation.KeyElementKind: "capacity", circulation.KeyLimit: 10})
	rows, _ = q.Payload[circulation.KeyResult].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "alpha" {
		t.Fatalf("query after the second rebuild = %#v", q.Payload)
	}
}

func TestCleanProjectionDirKeepsOnlyCurrent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, projectionDirName)
	for _, name := range []string{"meaning.sqlite", "meaning.1.sqlite", "meaning.1.sqlite-wal", "meaning.2.sqlite.tmp", "vocabulary.json"} {
		writeFileT(t, filepath.Join(dir, name), "x")
	}
	writeFileT(t, filepath.Join(dir, meaningCurrentPointer), "meaning.1.sqlite\n")
	cleanProjectionDir(root)
	for name, want := range map[string]bool{
		"meaning.sqlite": false, "meaning.1.sqlite": true, "meaning.1.sqlite-wal": true,
		"meaning.2.sqlite.tmp": false, "vocabulary.json": true, meaningCurrentPointer: true,
	} {
		if got := existsT(filepath.Join(dir, name)); got != want {
			t.Fatalf("%s exists=%v, want %v", name, got, want)
		}
	}
}

func journalModeOf(t *testing.T, path string, open func(string) (*sql.DB, error)) string {
	t.Helper()
	db, err := open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func TestProjectionOpensInWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.sqlite")
	if got := journalModeOf(t, path, openSQLite); got != "wal" {
		t.Fatalf("projection journal mode = %q, want wal", got)
	}
}

func TestBuildDatabaseUsesDeleteJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.sqlite")
	if got := journalModeOf(t, path, openSQLiteForBuild); got != "delete" {
		t.Fatalf("build journal mode = %q, want delete", got)
	}
	// once closed, a built file has no companion: it can be published as it is
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if existsT(path + suffix) {
			t.Fatalf("a built database left %s behind", suffix)
		}
	}
}

func TestRebuildLeavesNoCompanionFiles(t *testing.T) {
	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	writeContextDescriptor(t, ctxDir)
	writeFileT(t, filepath.Join(ctxDir, "capacity", "alpha.json"), capDoc("alpha", "one"))
	rebuildFull(t, l, commCh, "w-1")
	cur := dbPath(ctxDir)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if existsT(cur + suffix) {
			t.Fatalf("the published generation has a %s file before any connection opened it", suffix)
		}
	}
	if got := journalModeOf(t, cur, openSQLite); got != "wal" {
		t.Fatalf("after the first open the published file is in %q mode, want wal", got)
	}
}

func TestWALFileStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.sqlite")
	db, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(a INTEGER, b TEXT)`); err != nil {
		t.Fatal(err)
	}
	var biggest int64
	for batch := 0; batch < 300; batch++ {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 500; i++ {
			if _, err := tx.Exec(`INSERT INTO t VALUES(?,?)`, batch*500+i, "some text of a realistic length for a projection row"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if batch%50 == 49 {
			checkpointProjection(db)
		}
		if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > biggest {
			biggest = fi.Size()
		}
	}
	if biggest > walSizeLimitBytes {
		t.Fatalf("the write-ahead log reached %d bytes, above the %d limit", biggest, walSizeLimitBytes)
	}
}

// shortenOldGenerationGrace makes replaced generations disappear quickly in tests.
func shortenOldGenerationGrace(t *testing.T) {
	t.Helper()
	prev := oldGenerationGrace
	oldGenerationGrace = 20 * time.Millisecond
	t.Cleanup(func() { oldGenerationGrace = prev })
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
