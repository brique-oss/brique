/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */
package matter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
)

func TestMatterTruncateLinesAndGuards(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingLines, "one\ntwo\nthree\n")
	l.capMatterTruncate(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "truncate-lines", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: revision,
			circulation.KeyExpectedByteSize: int64(14),
			circulation.KeyTruncateTo:       map[string]any{circulation.KeyUnit: circulation.ValueFramingLines, circulation.KeySize: int64(2)},
		},
	}})
	response := recvMatterLoopMsg(t, comm, "truncate")
	if response.Response.Status != circulation.ValueStatusOK || response.Response.Payload[circulation.KeyTotalBytes] != int64(8) || response.Response.Payload[circulation.KeyTotalLines] != int64(2) {
		t.Fatalf("truncate response: %#v", response)
	}
	payload, err := os.ReadFile(filepath.Join(root, matterDirName, "log.log"))
	if err != nil || string(payload) != "one\ntwo\n" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}

	l.capMatterTruncate(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "truncate-stale", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: revision,
			circulation.KeyExpectedByteSize: int64(14), circulation.KeyTruncateTo: map[string]any{circulation.KeySize: int64(0)},
		},
	}})
	if stale := recvMatterLoopMsg(t, comm, "stale truncate"); stale.Response.Status != circulation.ValueStatusError {
		t.Fatalf("stale truncate should fail: %#v", stale)
	}
}

func TestMatterTruncateLegacyLineDescriptorComputesCount(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingLines, "one\ntwo\nthree\n")
	descriptorPath := filepath.Join(root, matterDirName, "log"+matterDescriptorSuffix)
	doc, err := readJSONMapFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	delete(doc[circulation.KeyBrique].(map[string]any), circulation.KeySubstanceLineCount)
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	entry, _ := l.catalogGetMatter("log")
	delete(entry.Brique, circulation.KeySubstanceLineCount)
	l.catalogSetMatter("log", entry)
	l.capMatterTruncate(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "truncate-legacy-lines", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyExpectedRevision: revision, circulation.KeyExpectedByteSize: int64(14),
			circulation.KeyTruncateTo: map[string]any{circulation.KeyUnit: circulation.ValueFramingLines, circulation.KeySize: int64(2)},
		},
	}})
	if response := recvMatterLoopMsg(t, comm, "legacy line truncate"); response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("truncate failed: %#v", response)
	}
}
