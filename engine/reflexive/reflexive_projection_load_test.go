package reflexive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

// responseRouter delivers each response to the caller that is waiting for it,
// so that several goroutines can call the loop at once.
type responseRouter struct {
	mu      sync.Mutex
	waiters map[string]chan circulation.Response
}

func newResponseRouter(src <-chan circulation.Message) *responseRouter {
	r := &responseRouter{waiters: map[string]chan circulation.Response{}}
	go func() {
		for m := range src {
			r.mu.Lock()
			ch := r.waiters[m.Response.IntentionID]
			delete(r.waiters, m.Response.IntentionID)
			r.mu.Unlock()
			if ch != nil {
				ch <- m.Response
			}
		}
	}()
	return r
}

func (r *responseRouter) call(id string, run func(circulation.Message), capName string, params map[string]any) (circulation.Response, bool) {
	ch := make(chan circulation.Response, 1)
	r.mu.Lock()
	r.waiters[id] = ch
	r.mu.Unlock()
	run(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: shared.RootContextID, Cap: capName, Type: circulation.ValueTypeReflexive},
		From:        circulation.Address{Context: shared.RootContextID},
		Params:      params,
	}})
	select {
	case resp := <-ch:
		return resp, true
	case <-time.After(10 * time.Second):
		return circulation.Response{}, false
	}
}

// Readers, a writer and rebuilds at the same time: no failed read, no stale
// generation left behind, a healthy database at the end.
func TestProjectionUnderLoad(t *testing.T) {
	prev := oldGenerationGrace
	oldGenerationGrace = 100 * time.Millisecond
	t.Cleanup(func() { oldGenerationGrace = prev })

	l, commCh, ctxDir := newReflexiveSQLiteHarness(t, shared.RootContextID)
	t.Cleanup(l.closeProjection)
	router := newResponseRouter(commCh)
	writeContextDescriptor(t, ctxDir)
	for i := 0; i < 300; i++ {
		writeFileT(t, filepath.Join(ctxDir, "capacity", fmt.Sprintf("c%03d.json", i)), capDoc(fmt.Sprintf("c%03d", i), fmt.Sprintf("d%d", i%10)))
	}
	if resp, ok := router.call("load-first", l.capSQLiteRebuild, "meaning.rebuild", map[string]any{circulation.KeyMode: circulation.ValueFull}); !ok || resp.Status != circulation.ValueStatusOK {
		t.Fatalf("the first rebuild failed: %#v (answered=%v)", resp.Error, ok)
	}
	var failures []string
	var failMu sync.Mutex
	fail := func(format string, args ...any) {
		failMu.Lock()
		failures = append(failures, fmt.Sprintf(format, args...))
		failMu.Unlock()
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads, passes, patches int64
	var seq int64
	next := func(prefix string) string { return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&seq, 1)) }

	for r := 0; r < 6; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, ok := router.call(next("read"), l.capSQLiteMeaningQuery, "meaning.query", map[string]any{
					circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50,
					circulation.KeyFilters: []any{map[string]any{circulation.KeyPath: "objective.description", circulation.KeyOp: "eq", circulation.KeyValue: "d3"}},
				})
				switch {
				case !ok:
					fail("a read got no answer")
				case resp.Status != circulation.ValueStatusOK:
					fail("a read failed: %#v", resp.Error)
				default:
					atomic.AddInt64(&reads, 1)
				}
			}
		}()
	}

	wg.Add(1)
	go func() { // the writer: new elements and sync passes, now and then a vocabulary patch
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			writeFileT(t, filepath.Join(ctxDir, "capacity", fmt.Sprintf("w%04d.json", i)), capDoc(fmt.Sprintf("w%04d", i), "written"))
			if _, err := l.syncProjection(false); err != nil {
				// a pass that spans a rebuild is replaced, and may find its old pool closed
				msg := err.Error()
				if !strings.Contains(msg, errSyncReplaced.Error()) && !strings.Contains(msg, "database is closed") {
					fail("a sync pass failed: %v", err)
				}
			} else {
				atomic.AddInt64(&passes, 1)
			}
			if i%5 == 0 {
				resp, ok := router.call(next("patch"), l.capSQLiteVocabularyPatch, "vocabulary.patch", map[string]any{
					circulation.KeyPatch: map[string]any{"functional": map[string]any{"load": []any{fmt.Sprintf("w%d", i)}}},
				})
				if !ok || resp.Status != circulation.ValueStatusOK {
					fail("a vocabulary patch failed: %#v (answered=%v)", resp.Error, ok)
				} else {
					atomic.AddInt64(&patches, 1)
				}
			}
		}
	}()

	for i := 0; i < 4; i++ {
		time.Sleep(400 * time.Millisecond)
		resp, ok := router.call(next("rebuild"), l.capSQLiteRebuild, "meaning.rebuild", map[string]any{circulation.KeyMode: circulation.ValueFull})
		if !ok || resp.Status != circulation.ValueStatusOK {
			fail("a rebuild failed: %#v (answered=%v)", resp.Error, ok)
		}
	}
	close(stop)
	wg.Wait()

	failMu.Lock()
	for i, f := range failures {
		if i >= 10 {
			break
		}
		t.Error(f)
	}
	nf := len(failures)
	failMu.Unlock()
	if nf > 0 {
		t.Fatalf("%d failures (reads=%d passes=%d patches=%d)", nf, reads, passes, patches)
	}
	if reads == 0 || passes == 0 || patches == 0 {
		t.Fatalf("the load did not run: reads=%d passes=%d patches=%d", reads, passes, patches)
	}

	time.Sleep(500 * time.Millisecond) // past the grace period: old generations are gone
	dir := filepath.Join(ctxDir, projectionDirName)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var dbs []string
	for _, e := range ents {
		if projectionLeftoverRE.MatchString(e.Name()) && strings.HasSuffix(e.Name(), ".sqlite") {
			dbs = append(dbs, e.Name())
		}
	}
	if len(dbs) != 1 {
		t.Fatalf("expected one projection generation file, found %v", dbs)
	}
	db, err := openSQLite(dbPath(ctxDir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q (err=%v)", check, err)
	}
	if fi, err := os.Stat(dbPath(ctxDir) + "-wal"); err == nil && fi.Size() > walSizeLimitBytes {
		t.Fatalf("write-ahead log of %d bytes", fi.Size())
	}
	t.Logf("reads=%d passes=%d patches=%d", reads, passes, patches)
}
