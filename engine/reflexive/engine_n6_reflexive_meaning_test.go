/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package reflexive_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func TestEngine_N6_REF_00_FixtureStartupAndReflexiveReady(t *testing.T) {
	h := newEngineN6Harness(t)

	if got := h.root.State(); got != shared.ContextRunning {
		t.Fatalf("root state=%v want %v", got, shared.ContextRunning)
	}

	if _, ok := h.reg.ResolveCh(shared.ContextAddr(shared.RootContextID)); !ok {
		t.Fatalf("root comm registry entry missing")
	}
	if _, ok := h.reg.ResolveCh(shared.ContextAddr(n6WorkspaceID)); !ok {
		t.Fatalf("workspace comm registry entry missing")
	}

	resp := callN6Reflexive(t, h, n6WorkspaceID, "n6-fixture-read-state", "read.state", map[string]any{
		circulation.KeyInclude: []any{circulation.KeyContext, circulation.KeyFamilies},
	})
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("workspace reflexive read.state should succeed: %#v", resp)
	}
	payload := mustPayloadMap(t, resp.Payload)
	ctx := mustPayloadMap(t, payload[circulation.KeyContext])
	if ctx[circulation.KeyContextId] != n6WorkspaceID {
		t.Fatalf("workspace read.state should target %s: %#v", n6WorkspaceID, ctx)
	}
	families := mustPayloadMap(t, payload[circulation.KeyFamilies])
	if len(families) == 0 {
		t.Fatalf("workspace read.state should expose families when requested: %#v", payload)
	}
}

func TestEngine_N6_REF_01_MeaningRebuildThenMeaningQuery(t *testing.T) {
	h := newEngineN6Harness(t)

	sendToN6Context(t, h, shared.RootContextID, mkEngineN6Intention(
		"n6-meaning-rebuild",
		shared.RootContextID,
		"meaning.rebuild",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyMode: circulation.ValueFull,
		},
	))

	rebuild := recvEngineN6Msg(t, h.sinkCh, "meaning.rebuild response")
	if rebuild.Kind != circulation.ValueKindResponse {
		t.Fatalf("rebuild kind=%q want response", rebuild.Kind)
	}
	if rebuild.Response.IntentionID != "n6-meaning-rebuild" {
		t.Fatalf("rebuild intention_id=%q want n6-meaning-rebuild", rebuild.Response.IntentionID)
	}
	if rebuild.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("rebuild status=%q want ok payload=%#v error=%#v", rebuild.Response.Status, rebuild.Response.Payload, rebuild.Response.Error)
	}
	t.Logf("meaning.rebuild response: %#v", rebuild.Response)

	rebuildPayload := mustPayloadMap(t, rebuild.Response.Payload)
	if rebuildPayload["mode"] != circulation.ValueFull {
		t.Fatalf("rebuild mode=%#v want %q", rebuildPayload["mode"], circulation.ValueFull)
	}
	if _, ok := rebuildPayload["total_elements_indexed"]; !ok {
		t.Fatalf("rebuild payload missing total_elements_indexed: %#v", rebuildPayload)
	}

	dbPath := filepath.Join(h.rootDir, "projection", "meaning.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("projection db should exist after rebuild at %s: %v", dbPath, err)
	}

	sendToN6Context(t, h, shared.RootContextID, mkEngineN6Intention(
		"n6-meaning-query-after-rebuild",
		shared.RootContextID,
		"meaning.query",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueCapacity,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
		},
	))

	query := recvEngineN6Msg(t, h.sinkCh, "meaning.query response after rebuild")
	if query.Kind != circulation.ValueKindResponse {
		t.Fatalf("query kind=%q want response", query.Kind)
	}
	if query.Response.IntentionID != "n6-meaning-query-after-rebuild" {
		t.Fatalf("query intention_id=%q want n6-meaning-query-after-rebuild", query.Response.IntentionID)
	}
	if query.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("query status=%q want ok payload=%#v error=%#v", query.Response.Status, query.Response.Payload, query.Response.Error)
	}
	t.Logf("meaning.query response after rebuild: %#v", query.Response)

	queryPayload := mustPayloadMap(t, query.Response.Payload)
	rows := mustPayloadArray(t, queryPayload, circulation.KeyResult)
	if len(rows) == 0 {
		t.Fatalf("meaning.query should return seeded rows: %#v", queryPayload)
	}
	if row := findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueCapacity, "sample.echo"); row == nil {
		t.Fatalf("meaning.query should include sample.echo row: %#v", rows)
	}
}

// N1b — EQ on a value nested two array levels deep (objective.ordre.entites,
// seeded as objective.ordre[].entites[] on sample.echo) must match, not just
// EXISTS. pathVariants previously only appended a trailing ".[]" suffix, so
// it could never produce the real indexed path_text ("objective.ordre.[].entites.[]")
// for a "[]" occurring anywhere but the very end of the path — EQ/LIKE
// silently returned no rows for any array-of-objects field beyond one level.
func TestEngine_N6_REF_01b_MeaningQueryEQMatchesNestedArrayValue(t *testing.T) {
	h := newEngineN6Harness(t)

	sendToN6Context(t, h, shared.RootContextID, mkEngineN6Intention(
		"n6-meaning-rebuild-nested",
		shared.RootContextID,
		"meaning.rebuild",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyMode: circulation.ValueFull,
		},
	))
	rebuild := recvEngineN6Msg(t, h.sinkCh, "meaning.rebuild response (nested array fixture)")
	if rebuild.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("rebuild status=%q want ok payload=%#v error=%#v", rebuild.Response.Status, rebuild.Response.Payload, rebuild.Response.Error)
	}

	sendToN6Context(t, h, shared.RootContextID, mkEngineN6Intention(
		"n6-meaning-query-nested-eq",
		shared.RootContextID,
		"meaning.query",
		circulation.ValueTypeReflexive,
		map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueCapacity,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "objective.ordre.entites",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "charlie",
				},
			},
		},
	))

	query := recvEngineN6Msg(t, h.sinkCh, "meaning.query response (nested array EQ)")
	if query.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("query status=%q want ok payload=%#v error=%#v", query.Response.Status, query.Response.Payload, query.Response.Error)
	}
	queryPayload := mustPayloadMap(t, query.Response.Payload)
	rows := mustPayloadArray(t, queryPayload, circulation.KeyResult)
	if row := findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueCapacity, "sample.echo"); row == nil {
		t.Fatalf("EQ on objective.ordre.entites=charlie (nested two array levels deep) should match sample.echo: %#v", rows)
	}
}

func TestEngine_N6_REF_02_MeaningQueryReadsSeededProjection(t *testing.T) {
	h := newEngineN6Harness(t)
	rebuildN6Meaning(t, h, "n6-meaning-rebuild-seeded")

	t.Run("before_rebuild_is_unavailable", func(t *testing.T) {
		h2 := newEngineN6Harness(t)
		resp := queryN6Meaning(t, h2, "n6-meaning-query-before-rebuild", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
		})
		if resp.Status != circulation.ValueStatusError || resp.Error == nil || resp.Error.Code != circulation.ValueCodeUnavailable {
			t.Fatalf("meaning.query before rebuild should be unavailable: %#v", resp)
		}
	})

	t.Run("kind_and_ctx_filters", func(t *testing.T) {
		docResp := queryN6Meaning(t, h, "n6-meaning-query-documents", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
		})
		t.Logf("meaning.query documents response: %#v", docResp)
		docRows := mustMeaningResultRows(t, docResp)
		if len(docRows) < 2 {
			t.Fatalf("document meaning.query should return seeded documents: %#v", docRows)
		}
		if row := findMeaningQueryRow(docRows, n6WorkspaceID, circulation.ValueDocument, "overview"); row == nil {
			t.Fatalf("document meaning.query should include overview: %#v", docRows)
		}
		if row := findMeaningQueryRow(docRows, n6WorkspaceID, circulation.ValueDocument, "guide"); row == nil {
			t.Fatalf("document meaning.query should include guide: %#v", docRows)
		}

		capResp := queryN6Meaning(t, h, "n6-meaning-query-capacities", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueCapacity,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
		})
		t.Logf("meaning.query capacities response: %#v", capResp)
		capRows := mustMeaningResultRows(t, capResp)
		if len(capRows) < 2 {
			t.Fatalf("capacity meaning.query should return seeded capacities: %#v", capRows)
		}
		if row := findMeaningQueryRow(capRows, n6WorkspaceID, circulation.ValueCapacity, "sample.echo"); row == nil {
			t.Fatalf("capacity meaning.query should include sample.echo: %#v", capRows)
		}
		if row := findMeaningQueryRow(capRows, n6WorkspaceID, circulation.ValueCapacity, "sample.normalize"); row == nil {
			t.Fatalf("capacity meaning.query should include sample.normalize: %#v", capRows)
		}
	})

	t.Run("eq_on_array_logical_path", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-guide-tag", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "guide",
				},
			},
		})
		t.Logf("meaning.query filtered response: %#v", resp)
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 1 {
			t.Fatalf("filtered meaning.query should return exactly one guide row: %#v", rows)
		}
		if row := findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueDocument, "guide"); row == nil {
			t.Fatalf("filtered meaning.query should resolve guide by functional.tags: %#v", rows)
		}
	})

	t.Run("eq_on_scalar_nested_path", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-mimetype", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.mime_type",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "text/markdown",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("mime_type EQ should return both markdown documents: %#v", rows)
		}
	})

	t.Run("like_on_scalar_path", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-description-like", map[string]any{
			circulation.KeyCtxId: n6WorkspaceID,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "objective.description",
					circulation.KeyOp:    circulation.ValueOpLIKE,
					circulation.KeyValue: "%workspace%",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) == 0 {
			t.Fatalf("description LIKE should return seeded workspace elements")
		}
	})

	t.Run("array_match_any", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-tags-any", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"guide", "overview"},
					circulation.KeyMatch: circulation.ValueMatchAny,
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("tags ANY should return guide and overview: %#v", rows)
		}
	})

	t.Run("array_match_all", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-tags-all", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"guide", "projection"},
					circulation.KeyMatch: circulation.ValueMatchAll,
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 1 || findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueDocument, "guide") == nil {
			t.Fatalf("tags ALL should return only guide: %#v", rows)
		}
	})

	t.Run("array_match_all_lowercase", func(t *testing.T) {
		// match: "all" (lowercase) must behave identically to ValueMatchAll.
		resp := queryN6Meaning(t, h, "n6-meaning-query-tags-all-lc", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"guide", "projection"},
					circulation.KeyMatch: "all",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 1 || findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueDocument, "guide") == nil {
			t.Fatalf("tags ALL (lowercase) should return only guide: %#v", rows)
		}
	})

	t.Run("array_match_default", func(t *testing.T) {
		// missing match should default to ANY behavior.
		resp := queryN6Meaning(t, h, "n6-meaning-query-tags-default-match", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"guide", "overview"},
					// KeyMatch intentionally omitted — must default to ANY
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("missing match should default to ANY, returning guide and overview: %#v", rows)
		}
	})

	t.Run("combined_filters", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-combined", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueCapacity,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "objective.name",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "sample.echo",
				},
				map[string]any{
					circulation.KeyPath:  "objective.owner",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "engine_test_suite",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 1 || findMeaningQueryRow(rows, n6WorkspaceID, circulation.ValueCapacity, "sample.echo") == nil {
			t.Fatalf("combined filters should isolate sample.echo: %#v", rows)
		}
	})

	t.Run("limit_offset_order_by_name", func(t *testing.T) {
		fullResp := queryN6Meaning(t, h, "n6-meaning-query-documents-full-name", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyOrderBy:     circulation.ValueName,
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
		})
		fullRows := mustMeaningResultRows(t, fullResp)
		fullNames := resultNames(fullRows)
		if !sort.StringsAreSorted(fullNames) {
			t.Fatalf("document names should be sorted by name: %#v", fullNames)
		}

		pageResp := queryN6Meaning(t, h, "n6-meaning-query-documents-page", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID,
			circulation.KeyElementKind: circulation.ValueDocument,
			circulation.KeyOrderBy:     circulation.ValueName,
			circulation.KeyLimit:       1,
			circulation.KeyOffset:      1,
		})
		pageRows := mustMeaningResultRows(t, pageResp)
		if len(pageRows) != 1 {
			t.Fatalf("paged query should return exactly one row: %#v", pageRows)
		}
		pageNames := resultNames(pageRows)
		if len(fullNames) < 2 || pageNames[0] != fullNames[1] {
			t.Fatalf("paged query should align with ordered full result: full=%#v page=%#v", fullNames, pageNames)
		}
	})

	t.Run("order_by_kind", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-order-kind", map[string]any{
			circulation.KeyCtxId:   n6WorkspaceID,
			circulation.KeyOrderBy: circulation.ValueKind,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) < 4 {
			t.Fatalf("kind-ordered query should return multiple element kinds: %#v", rows)
		}
		prevKind := ""
		prevName := ""
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			kind, _ := row["element_type"].(string)
			name, _ := row["name"].(string)
			if prevKind > kind || (prevKind == kind && prevName > name) {
				t.Fatalf("rows should be ordered by kind then name: %#v", rows)
			}
			prevKind, prevName = kind, name
		}
	})

	t.Run("music_multi_context_same_name", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-music-same-name", map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.title",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "Get Lucky",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("global music query should return both contextualized Get Lucky matters: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr", circulation.ValueMatter, "track_get_lucky") == nil {
			t.Fatalf("global music query should include FR track_get_lucky: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_us", circulation.ValueMatter, "track_get_lucky") == nil {
			t.Fatalf("global music query should include US track_get_lucky: %#v", rows)
		}
	})

	t.Run("music_context_isolation", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-music-fr-only", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID + "/chart_fr",
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.artist",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "Daft Punk",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("FR music query should return two Daft Punk tracks: %#v", rows)
		}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if row["ctx_id"] != n6WorkspaceID+"/chart_fr" {
				t.Fatalf("FR-isolated query leaked another context: %#v", rows)
			}
		}
	})

	t.Run("exists_on_array_logical_path", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-genres-exists", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID + "/chart_fr",
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.classification.genres",
					circulation.KeyOp:    circulation.ValueOpEXISTS,
					circulation.KeyValue: "ignored-by-exists",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("genres EXISTS should return the two FR chart matters: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr", circulation.ValueMatter, "track_get_lucky") == nil {
			t.Fatalf("genres EXISTS should include FR track_get_lucky: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr", circulation.ValueMatter, "track_instant_crush") == nil {
			t.Fatalf("genres EXISTS should include FR track_instant_crush: %#v", rows)
		}
	})

	t.Run("exists_on_object_container_path", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-music-exists", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID + "/chart_fr",
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath: "functional.music",
					circulation.KeyOp:   circulation.ValueOpEXISTS,
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 2 {
			t.Fatalf("functional.music EXISTS should return the two FR chart matters: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr", circulation.ValueMatter, "track_get_lucky") == nil {
			t.Fatalf("functional.music EXISTS should include FR track_get_lucky: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr", circulation.ValueMatter, "track_instant_crush") == nil {
			t.Fatalf("functional.music EXISTS should include FR track_instant_crush: %#v", rows)
		}
	})

	t.Run("music_date_prefix_decade_like", func(t *testing.T) {
		req := map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.release.date",
					circulation.KeyOp:    circulation.ValueOpLIKE,
					circulation.KeyValue: "201%",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		}
		resp := queryN6Meaning(t, h, "n6-meaning-query-decade-2010s", req)
		t.Logf("meaning.query decade request: %#v", req)
		t.Logf("meaning.query decade response: %#v", resp)
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 4 {
			t.Fatalf("2010s LIKE query should return four tracks: %#v", rows)
		}
		want := []struct {
			ctxID string
			name  string
		}{
			{n6WorkspaceID + "/chart_fr", "track_get_lucky"},
			{n6WorkspaceID + "/chart_fr", "track_instant_crush"},
			{n6WorkspaceID + "/chart_us", "track_get_lucky"},
			{n6WorkspaceID + "/chart_us/regional", "track_starboy"},
		}
		for _, item := range want {
			if findMeaningQueryRow(rows, item.ctxID, circulation.ValueMatter, item.name) == nil {
				t.Fatalf("2010s LIKE query missing %s in %s: %#v", item.name, item.ctxID, rows)
			}
		}
	})

	t.Run("music_combined_filters_and_batch_read", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-music-selection", map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.artist",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "Daft Punk",
				},
				map[string]any{
					circulation.KeyPath:  "functional.music.genre",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "electronic",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 4 {
			t.Fatalf("Daft Punk + electronic should return four chart matters across contexts: %#v", rows)
		}

		frIDs := []string{}
		usIDs := []string{}
		editorialIDs := []string{}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			name, _ := row["name"].(string)
			ctxID, _ := row["ctx_id"].(string)
			switch ctxID {
			case n6WorkspaceID + "/chart_fr":
				frIDs = append(frIDs, name)
			case n6WorkspaceID + "/chart_us":
				usIDs = append(usIDs, name)
			case n6WorkspaceID + "/chart_fr/editorial":
				editorialIDs = append(editorialIDs, name)
			}
		}
		sort.Strings(frIDs)
		sort.Strings(usIDs)
		sort.Strings(editorialIDs)
		if len(frIDs) != 2 || len(usIDs) != 1 || len(editorialIDs) != 1 {
			t.Fatalf("selection should split into 2 FR ids, 1 US id and 1 editorial id: fr=%#v us=%#v editorial=%#v", frIDs, usIDs, editorialIDs)
		}

		frResp := queryN6MatterBatch(t, h, n6WorkspaceID+"/chart_fr", "n6-matter-read-batch-fr", map[string]any{
			circulation.KeyMatterIDs: []string{frIDs[0], frIDs[1]},
			circulation.KeyReadMode:  "functional|brique",
		})
		if frResp.Status != circulation.ValueStatusOK {
			t.Fatalf("FR matter.read_batch status=%q want ok payload=%#v error=%#v", frResp.Status, frResp.Payload, frResp.Error)
		}
		frPayload := mustPayloadMap(t, frResp.Payload)
		frResult := mustPayloadArray(t, frPayload, circulation.KeyResult)
		if len(frResult) != 2 {
			t.Fatalf("FR matter.read_batch should return two results: %#v", frResult)
		}

		usResp := queryN6Matter(t, h, n6WorkspaceID+"/chart_us", "n6-matter-read-us", map[string]any{
			circulation.KeyMatterID: usIDs[0],
			circulation.KeyReadMode: "functional|brique",
		})
		if usResp.Status != circulation.ValueStatusOK {
			t.Fatalf("US matter.read status=%q want ok payload=%#v error=%#v", usResp.Status, usResp.Payload, usResp.Error)
		}
		usPayload := mustPayloadMap(t, usResp.Payload)
		if usPayload[circulation.KeyMatterID] != usIDs[0] {
			t.Fatalf("US matter.read should echo selected matter id: %#v", usPayload)
		}
		functional := mustPayloadMap(t, usPayload[circulation.KeyFunctional])
		music := mustPayloadMap(t, functional["music"])
		chart := mustPayloadMap(t, music["chart"])
		if chart["market"] != "us" || music["title"] != "Get Lucky" {
			t.Fatalf("US matter.read should preserve contextualized music payload: %#v", usPayload)
		}
	})

	t.Run("music_complex_multi_context_nested_selection", func(t *testing.T) {
		req := map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.chart.snapshot_date",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "2026-03-28",
				},
				map[string]any{
					circulation.KeyPath:  "functional.music.classification.genres",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"electronic", "pop"},
					circulation.KeyMatch: circulation.ValueMatchAny,
				},
				map[string]any{
					circulation.KeyPath:  "functional.music.classification.styles",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"nu-disco", "synthwave"},
					circulation.KeyMatch: circulation.ValueMatchAny,
				},
				map[string]any{
					circulation.KeyPath:  "subjective.emojis",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"🔥", "🌙"},
					circulation.KeyMatch: circulation.ValueMatchAny,
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		}
		resp := queryN6Meaning(t, h, "n6-meaning-query-music-complex", req)
		t.Logf("meaning.query complex request: %#v", req)
		t.Logf("meaning.query complex response: %#v", resp)
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 4 {
			t.Fatalf("complex nested selection should return four items across contexts: %#v", rows)
		}
		want := []struct {
			ctxID string
			name  string
		}{
			{n6WorkspaceID + "/chart_fr", "track_get_lucky"},
			{n6WorkspaceID + "/chart_us", "track_blinding_lights"},
			{n6WorkspaceID + "/chart_us", "track_get_lucky"},
			{n6WorkspaceID + "/chart_us/regional", "track_starboy"},
		}
		for _, item := range want {
			if findMeaningQueryRow(rows, item.ctxID, circulation.ValueMatter, item.name) == nil {
				t.Fatalf("complex nested selection missing %s in %s: %#v", item.name, item.ctxID, rows)
			}
		}
	})

	t.Run("subjective_tags_precise_selection", func(t *testing.T) {
		req := map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "subjective.tags",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: []any{"sunrise", "midnight", "festival"},
					circulation.KeyMatch: circulation.ValueMatchAny,
				},
				map[string]any{
					circulation.KeyPath:  "functional.music.chart.snapshot_date",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "2026-03-28",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
			circulation.KeyLimit:   20,
			circulation.KeyOffset:  0,
		}
		resp := queryN6Meaning(t, h, "n6-meaning-query-subjective-tags", req)
		t.Logf("meaning.query subjective-tags request: %#v", req)
		t.Logf("meaning.query subjective-tags response: %#v", resp)
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 3 {
			t.Fatalf("subjective tag selection should return three contextualized matters: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_us", circulation.ValueMatter, "track_get_lucky") == nil {
			t.Fatalf("subjective tag selection should include US track_get_lucky: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_fr/editorial", circulation.ValueMatter, "track_veridis_quo") == nil {
			t.Fatalf("subjective tag selection should include editorial track_veridis_quo: %#v", rows)
		}
		if findMeaningQueryRow(rows, n6WorkspaceID+"/chart_us/regional", circulation.ValueMatter, "track_starboy") == nil {
			t.Fatalf("subjective tag selection should include regional track_starboy: %#v", rows)
		}
	})

	t.Run("music_deep_context_path_is_visible", func(t *testing.T) {
		resp := queryN6Meaning(t, h, "n6-meaning-query-deep-context", map[string]any{
			circulation.KeyCtxId:       n6WorkspaceID + "/chart_us/regional",
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.title",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "Starboy",
				},
			},
		})
		rows := mustMeaningResultRows(t, resp)
		if len(rows) != 1 {
			t.Fatalf("deep-context query should return exactly one Starboy matter: %#v", rows)
		}
		row := findMeaningQueryRow(rows, n6WorkspaceID+"/chart_us/regional", circulation.ValueMatter, "track_starboy")
		if row == nil {
			t.Fatalf("deep-context query should expose full contextual path: %#v", rows)
		}
	})

	t.Run("music_pagination_strict_no_duplicates", func(t *testing.T) {
		base := map[string]any{
			circulation.KeyElementKind: circulation.ValueMatter,
			circulation.KeyFilters: []any{
				map[string]any{
					circulation.KeyPath:  "functional.music.chart.snapshot_date",
					circulation.KeyOp:    circulation.ValueOpEQ,
					circulation.KeyValue: "2026-03-28",
				},
			},
			circulation.KeyOrderBy: circulation.ValueName,
		}
		fullReq := map[string]any{
			circulation.KeyElementKind: base[circulation.KeyElementKind],
			circulation.KeyFilters:     base[circulation.KeyFilters],
			circulation.KeyOrderBy:     base[circulation.KeyOrderBy],
			circulation.KeyLimit:       20,
			circulation.KeyOffset:      0,
		}
		fullRows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-meaning-query-music-page-full", fullReq))
		fullKeys := resultKeys(fullRows)
		if len(fullKeys) < 5 {
			t.Fatalf("expected enough music rows for strict pagination: %#v", fullRows)
		}

		seen := map[string]bool{}
		collected := []string{}
		for i := 0; i < len(fullKeys); i++ {
			pageReq := map[string]any{
				circulation.KeyElementKind: base[circulation.KeyElementKind],
				circulation.KeyFilters:     base[circulation.KeyFilters],
				circulation.KeyOrderBy:     base[circulation.KeyOrderBy],
				circulation.KeyLimit:       1,
				circulation.KeyOffset:      i,
			}
			pageRows := mustMeaningResultRows(t, queryN6Meaning(t, h, "n6-meaning-query-music-page-"+time.Now().UTC().Format("150405.000000000"), pageReq))
			if len(pageRows) != 1 {
				t.Fatalf("strict pagination page %d should return one row: %#v", i, pageRows)
			}
			key := resultKeys(pageRows)[0]
			if seen[key] {
				t.Fatalf("pagination should not duplicate rows across pages: %q", key)
			}
			seen[key] = true
			collected = append(collected, key)
		}
		if len(collected) != len(fullKeys) {
			t.Fatalf("pagination should collect same cardinality as full query: full=%#v collected=%#v", fullKeys, collected)
		}
		for i := range fullKeys {
			if collected[i] != fullKeys[i] {
				t.Fatalf("pagination order should match full query: full=%#v collected=%#v", fullKeys, collected)
			}
		}
	})
}
