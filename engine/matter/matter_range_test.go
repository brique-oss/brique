/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/circulation"
)

func TestMatterReadByteAndLineRanges(t *testing.T) {
	l, comm, _, _ := createAppendFixture(t, circulation.ValueFramingLines, "one\ntwo\nthree\n")

	for _, test := range []struct {
		name     string
		rangeArg map[string]any
		want     string
	}{
		{name: "bytes", rangeArg: map[string]any{circulation.KeyUnit: circulation.ValueFramingBytes, circulation.KeyOffset: int64(4), circulation.KeyLength: int64(3)}, want: "two"},
		{name: "line tail", rangeArg: map[string]any{circulation.KeyUnit: circulation.ValueFramingLines, circulation.KeyTail: int64(2)}, want: "two\nthree\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			l.capMatterRead(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
				IntentionID: "read-range-" + test.name,
				Params: map[string]any{
					circulation.KeyMatterID: "log", circulation.KeyReadMode: "data",
					circulation.KeyDataRange: test.rangeArg,
				},
			}})
			response := recvMatterLoopMsg(t, comm, "read range")
			if response.Response.Status != circulation.ValueStatusOK {
				t.Fatalf("read failed: %#v", response)
			}
			data := response.Response.Payload[circulation.KeyData].(map[string]any)
			if got := data[circulation.KeyBytes]; got != test.want {
				t.Fatalf("bytes=%q want=%q data=%#v", got, test.want, data)
			}
		})
	}
}

func TestLineRangeHandlesRecordsLargerThanScannerBuffer(t *testing.T) {
	payload := strings.Repeat("x", 128*1024) + "\nlast\n"
	l, comm, _, _ := createAppendFixture(t, circulation.ValueFramingLines, payload)
	l.capMatterRead(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "read-large-line", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyReadMode: "data",
			circulation.KeyDataRange: map[string]any{circulation.KeyUnit: circulation.ValueFramingLines, circulation.KeyTail: int64(1)},
		},
	}})
	response := recvMatterLoopMsg(t, comm, "large line range")
	if response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read failed: %#v", response)
	}
	data := response.Response.Payload[circulation.KeyData].(map[string]any)
	if data[circulation.KeyBytes] != "last\n" {
		t.Fatalf("tail=%#v", data)
	}
}

func TestParseMatterDataRangeRejectsAmbiguity(t *testing.T) {
	_, err := parseMatterDataRange(map[string]any{circulation.KeyDataRange: map[string]any{
		circulation.KeyUnit: circulation.ValueFramingBytes, circulation.KeyTail: 1, circulation.KeyOffset: 0,
	}}, 10, 0, circulation.ValueFramingBytes)
	if err == nil {
		t.Fatal("tail plus offset must fail")
	}
}

func TestLineIndexRebuildAndAppendExtension(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingLines, "one\ntwo\nthree\n")
	payloadPath := filepath.Join(root, matterDirName, "log.log")
	readTail := func(id string) {
		l.capMatterRead(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: id, Params: map[string]any{
				circulation.KeyMatterID: "log", circulation.KeyReadMode: "data",
				circulation.KeyDataRange: map[string]any{circulation.KeyUnit: circulation.ValueFramingLines, circulation.KeyTail: int64(1)},
			},
		}})
		if response := recvMatterLoopMsg(t, comm, "line index read"); response.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("line read failed: %#v", response)
		}
	}
	readTail("build-index")
	indexPath := lineIndexPath(payloadPath)
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("line index not created: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	readTail("rebuild-index")
	malformed, err := json.Marshal(matterLineIndex{Version: 1, Revision: revision, TotalBytes: int64(len("one\ntwo\nthree\n")), TotalLines: 3, Stride: defaultLineIndexStride, Points: []lineCheckpoint{{Line: 0, Byte: 0}, {Line: 256, Byte: 999999}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	readTail("rebuild-structurally-invalid-index")

	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "extend-index", Params: map[string]any{
			circulation.KeyMatterID: "log", circulation.KeyData: "four\n", circulation.KeyExpectedRevision: revision,
		},
	}})
	response := recvMatterLoopMsg(t, comm, "append extending index")
	if response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("append failed: %#v", response)
	}
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index matterLineIndex
	if err := json.Unmarshal(encoded, &index); err != nil {
		t.Fatalf("invalid rebuilt index: %v", err)
	}
	if index.Revision != response.Response.Payload[circulation.KeyRevision] || index.TotalLines != 4 || index.TotalBytes != int64(len("one\ntwo\nthree\nfour\n")) {
		t.Fatalf("index was not extended: %#v", index)
	}
}
