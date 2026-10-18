/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"testing"

	"brique_engine/circulation"
)

func TestMatterLifecycleRevisionAndSizeGuards(t *testing.T) {
	l, comm, _, revision := createAppendFixture(t, circulation.ValueFramingBytes, "base")

	l.capMatterClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "clone-stale", Params: map[string]any{
			circulation.KeySourceMatterID: "log", circulation.KeyTargetMatterID: "clone-stale",
			circulation.KeyExpectedSourceRevision: revision + 1,
		},
	}})
	if response := recvMatterLoopMsg(t, comm, "stale clone"); response.Response.Status != circulation.ValueStatusError {
		t.Fatalf("stale clone should fail: %#v", response)
	}
	l.capMatterClone(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "clone-guarded", Params: map[string]any{
			circulation.KeySourceMatterID: "log", circulation.KeyTargetMatterID: "clone-ok",
			circulation.KeyExpectedSourceRevision: revision, circulation.KeyExpectedSourceByteSize: int64(4),
		},
	}})
	if response := recvMatterLoopMsg(t, comm, "guarded clone"); response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("guarded clone failed: %#v", response)
	}

	derive := func(id, target string, expectedRevision int64) circulation.Message {
		l.capMatterDerive(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: id, Params: map[string]any{
				circulation.KeyTargetMatterID: target,
				circulation.KeyMatter:         map[string]any{circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}},
				circulation.KeySourceGuards: []any{map[string]any{
					circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: expectedRevision, circulation.KeyExpectedByteSize: int64(4),
				}},
			},
		}})
		return recvMatterLoopMsg(t, comm, id)
	}
	if response := derive("derive-stale", "derived-stale", revision+1); response.Response.Status != circulation.ValueStatusError {
		t.Fatalf("stale derive should fail: %#v", response)
	}
	if response := derive("derive-guarded", "derived-ok", revision); response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("guarded derive failed: %#v", response)
	}

	l.capMatterDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "delete-stale", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: revision + 1, circulation.KeyExpectedByteSize: int64(4),
		},
	}})
	if response := recvMatterLoopMsg(t, comm, "stale delete"); response.Response.Status != circulation.ValueStatusError {
		t.Fatalf("stale delete should fail: %#v", response)
	}
	l.capMatterDelete(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "delete-guarded", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: revision, circulation.KeyExpectedByteSize: int64(4),
		},
	}})
	if response := recvMatterLoopMsg(t, comm, "guarded delete"); response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("guarded delete failed: %#v", response)
	}
}
