/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package replay

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestServiceBindsGroupCommitRetentionAndTransitions(t *testing.T) {
	file, err := os.OpenFile(t.TempDir()+"/replay.log", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(file, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ledger, DefaultBatchOptions(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{AudienceInstanceID: "prod-a", PrincipalID: "partner-a", IntentionID: "123e4567-e89b-42d3-a456-426614174000"}
	expiresAt := time.Unix(1_800_000_000, 0).UTC()
	reserved, err := service.Reserve(context.Background(), key, testHash(1), expiresAt)
	if err != nil || reserved.Kind != ReservationNew || !reserved.Entry.RetainUntil.Equal(expiresAt.Add(time.Minute)) {
		t.Fatalf("unexpected reservation: %+v err=%v", reserved, err)
	}
	if err := service.MarkDispatched(key, reserved.Entry.RequestHash); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRunning(key, reserved.Entry.RequestHash, []byte("exact-envelope")); err != nil {
		t.Fatal(err)
	}
	entry, found := service.Lookup(key)
	if !found || entry.State != StateRunning || string(entry.LatestRunning) != "exact-envelope" {
		t.Fatalf("unexpected durable entry: %+v", entry)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestServiceRejectsUnsafeRetention(t *testing.T) {
	file, err := os.OpenFile(t.TempDir()+"/replay.log", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(file, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ledger, DefaultBatchOptions(), MinimumRetentionAfterExpiry-time.Nanosecond); err == nil {
		t.Fatal("retention shorter than the protocol minimum was accepted")
	}
	_ = ledger.Close()
}
