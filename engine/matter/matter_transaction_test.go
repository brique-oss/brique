/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
)

func TestMatterTransactionCommitAndRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m.data.tmp"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "m.matter.json.tmp"), []byte(`{"revision":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	steps := []matterTransactionStep{
		{Temporary: "m.data.tmp", Final: "m.data"},
		{Temporary: "m.matter.json.tmp", Final: "m.matter.json"},
	}
	if err := commitMatterTransaction(root, "m", circulation.ValueOpWrite, steps); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "m.data")); string(got) != "new" {
		t.Fatalf("payload=%q", got)
	}
	if _, err := os.Stat(matterTransactionPath(root, "m")); !os.IsNotExist(err) {
		t.Fatalf("journal should be removed: %v", err)
	}

	// Simulate interruption after the first rename: final payload exists while
	// the descriptor is still staged and the journal remains.
	if err := os.WriteFile(filepath.Join(root, "m.data"), []byte("next"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "m.matter.json.tmp"), []byte(`{"revision":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	record := []byte(`{"version":1,"matter_id":"m","op":"write","steps":[{"temporary":"m.data.tmp","final":"m.data"},{"temporary":"m.matter.json.tmp","final":"m.matter.json"}]}`)
	if err := os.WriteFile(matterTransactionPath(root, "m"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverMatterTransactions(root); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "m.matter.json")); string(got) != `{"revision":3}` {
		t.Fatalf("descriptor=%q", got)
	}
}

func TestSubstanceStateLines(t *testing.T) {
	state, err := stateForPayload([]byte("one\ntwo\n"), circulation.ValueFramingLines)
	if err != nil {
		t.Fatal(err)
	}
	if state.Bytes != 8 || state.Lines != 2 {
		t.Fatalf("state=%+v", state)
	}
	if _, err := stateForPayload([]byte("unterminated"), circulation.ValueFramingLines); err == nil {
		t.Fatal("unterminated line framing must fail")
	}
}

func TestMatterDeleteTransactionCommitAndRecovery(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"m.matter.json", "m.data", "m.data.line-index.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := commitMatterDeleteTransaction(root, "m", []string{"m.data", "m.data.line-index.json", "m.matter.json"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"m.matter.json", "m.data", "m.data.line-index.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}

	// Simulate a crash after only the first removal.
	if err := os.WriteFile(filepath.Join(root, "m.matter.json"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "m.data"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := matterTransaction{Version: 1, MatterID: "m", Op: "delete", Deletes: []string{"m.data", "m.matter.json"}}
	if err := writeMatterTransactionRecord(root, record); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "m.data")); err != nil {
		t.Fatal(err)
	}
	if err := recoverMatterTransactions(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "m.matter.json")); !os.IsNotExist(err) {
		t.Fatalf("descriptor still exists after recovery: %v", err)
	}
}

func TestAppendTransactionRecoversPartialBlockWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m.data"), []byte("basepa"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "block"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	step := &matterAppendStep{Block: "block", Payload: "m.data", BaseBytes: 4, AppendBytes: 7}
	if err := applyAppendTransaction(root, step); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(filepath.Join(root, "m.data")); err != nil || string(payload) != "basepayload" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestMatterMutationRefusesRecoveryPendingJournal(t *testing.T) {
	l, comm, root, _ := createAppendFixture(t, circulation.ValueFramingBytes, "base")
	matterRoot := filepath.Join(root, matterDirName)
	if err := writeMatterTransactionRecord(matterRoot, matterTransaction{Version: 1, MatterID: "log", Op: "delete", Deletes: []string{"log.matter.json"}}); err != nil {
		t.Fatal(err)
	}
	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "append-pending", Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: "tail"},
	}})
	response := recvMatterLoopMsg(t, comm, "recovery pending refusal")
	if response.Response.Status != circulation.ValueStatusError || response.Response.Error == nil || response.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonRecoveryPending {
		t.Fatalf("mutation was not refused: %#v", response)
	}
	if payload, err := os.ReadFile(filepath.Join(matterRoot, "log.log")); err != nil || string(payload) != "base" {
		t.Fatalf("payload changed: %q err=%v", payload, err)
	}
}
