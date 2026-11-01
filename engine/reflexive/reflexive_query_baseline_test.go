package reflexive

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

// Baseline measurements for the access-rights work (SECURITY.md §12.2): cost of building the projection and of meaning.query on a synthetic
// instance. They are the reference the filtered discovery of phase 4 is compared
// with.
//
//	BRIQUE_BENCH_ELEMENTS=100000 go test ./reflexive -run '^$' -bench QueryBaseline -benchtime=200x
//
// The default is 10,000 elements so the benchmark stays cheap in CI.

func baselineElements() int {
	if v, err := strconv.Atoi(os.Getenv("BRIQUE_BENCH_ELEMENTS")); err == nil && v > 0 {
		return v
	}
	return 10000
}

type queryBaseline struct {
	l    *ReflexiveLoop
	out  chan junction.Delivery
	last circulation.Message
	n    int
}

func buildQueryBaseline(b *testing.B) *queryBaseline {
	b.Helper()
	root := b.TempDir()
	put := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	put("context.json", `{"brique":{"ctx_name":"r"}}`)
	n := baselineElements()
	const contexts = 100
	perCtx := n / contexts
	if perCtx < 1 {
		perCtx = 1
	}
	for c := 0; c < contexts; c++ {
		area := fmt.Sprintf("area%02d", c%10)
		ctx := fmt.Sprintf("%s/team%02d", area, c/10)
		put(area+"/context.json", `{"brique":{"ctx_name":"a"}}`) // every level is a context
		put(ctx+"/context.json", `{"brique":{"ctx_name":"c"}}`)
		for i := 0; i < perCtx; i++ {
			name := fmt.Sprintf("cap%04d", i)
			put(fmt.Sprintf("%s/capacity/%s.json", ctx, name),
				fmt.Sprintf(`{"brique":{"cap_name":%q,"kind":"dsl"},"objective":{"name":"%s.%s","description":"common description %d"},"functional":{"role":"role-%d"},"subjective":{}}`,
					name, ctx, name, i%50, i%7))
		}
	}
	out := make(chan junction.Delivery, 1024)
	l := &ReflexiveLoop{
		frame: &junction.ContextRegistry{ContextDir: root, CtxId: shared.RootContextID,
			FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: out}},
		done: make(chan struct{}),
	}
	b.Cleanup(func() { close(l.done) })

	// Age the files so the racy-timestamp guard does not distort the build.
	old := time.Now().Add(-time.Minute)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})

	t0 := time.Now()
	res, err := l.syncProjection(false)
	if err != nil {
		b.Fatal(err)
	}
	d := time.Since(t0)
	if res.Added < n/2 {
		b.Fatalf("projection build indexed only %d of ~%d elements", res.Added, n)
	}
	b.Logf("projection build: %d elements in %v (%.0f elements/s)", res.Added, d, float64(res.Added)/d.Seconds())

	q := &queryBaseline{l: l, out: out}
	go func() {
		for d := range out {
			if msg, ok := d.Message(); ok {
				q.last = msg
			}
		}
	}()
	return q
}

func (q *queryBaseline) run(b *testing.B, params map[string]any) int {
	q.n++
	id := fmt.Sprintf("q-%d", q.n)
	q.last = circulation.Message{}
	q.l.capSQLiteMeaningQuery(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id, To: circulation.Address{Context: shared.RootContextID, Cap: "meaning.query", Type: circulation.ValueTypeReflexive},
		From: circulation.Address{Context: shared.RootContextID}, Params: params,
	}})
	// emitToComm is synchronous; give the drain goroutine a moment to publish.
	deadline := time.Now().Add(2 * time.Second)
	for q.last.Response.IntentionID != id && time.Now().Before(deadline) {
		time.Sleep(10 * time.Microsecond)
	}
	if q.last.Response.Status != circulation.ValueStatusOK {
		b.Fatalf("query failed: %#v", q.last.Response.Error)
	}
	rows, _ := q.last.Response.Payload[circulation.KeyResult].([]any)
	return len(rows)
}

func BenchmarkQueryBaseline(b *testing.B) {
	q := buildQueryBaseline(b)
	filter := func(path, value string) []any {
		return []any{map[string]any{circulation.KeyPath: path, circulation.KeyOp: "eq", circulation.KeyValue: value}}
	}
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"point_by_name", map[string]any{circulation.KeyElementKind: "capacity",
			circulation.KeyFilters: filter("objective.name", "area03/team02.cap0007")}},
		{"one_context_listing", map[string]any{circulation.KeyElementKind: "capacity",
			circulation.KeyCtxId: "/root/area03/team02", circulation.KeyLimit: 50}},
		{"subtree_listing", map[string]any{circulation.KeyElementKind: "capacity",
			circulation.KeyCtxId: "/root/area03", circulation.KeyCtxMode: circulation.ValueCtxModeSubtree, circulation.KeyLimit: 50}},
		{"common_value_limit50", map[string]any{circulation.KeyElementKind: "capacity",
			circulation.KeyFilters: filter("objective.description", "common description 3"), circulation.KeyLimit: 50}},
		{"instance_wide_limit50", map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyLimit: 50}},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			rows := q.run(b, c.params)
			b.ReportMetric(float64(rows), "rows")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				q.run(b, c.params)
			}
		})
	}
}
