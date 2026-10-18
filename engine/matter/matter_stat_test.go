/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"testing"

	"brique_engine/circulation"
)

func TestMatterStatReturnsCommittedDimensions(t *testing.T) {
	l, comm, _ := newMatterLoopHarness(t)
	l.capMatterCreate(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "create-stat",
		Params: map[string]any{
			circulation.KeyMatterID: "journal",
			circulation.KeyMatter: map[string]any{
				circulation.KeyBrique: map[string]any{
					circulation.KeySubstanceMode:    circulation.ValueModeBrique,
					circulation.KeySubstanceFraming: circulation.ValueFramingLines,
				},
				circulation.KeyFunctional: map[string]any{circulation.KeyFormat: "jsonl"},
			},
			circulation.KeyPayload: "one\ntwo\n",
		},
	}})
	created := recvMatterLoopMsg(t, comm, "create stat matter")
	if created.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("create failed: %#v", created)
	}

	l.capMatterStat(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "stat",
		Params:      map[string]any{circulation.KeyMatterID: "journal"},
	}})
	stat := recvMatterLoopMsg(t, comm, "stat matter")
	payload := stat.Response.Payload
	if payload[circulation.KeyTotalBytes] != int64(8) || payload[circulation.KeyTotalLines] != int64(2) {
		t.Fatalf("unexpected stat payload: %#v", payload)
	}
}
