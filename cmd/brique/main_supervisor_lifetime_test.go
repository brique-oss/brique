/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSupervisorLifetimeReportsWriterLoss(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lost, cleanup, err := startSupervisorLifetime(strconv.FormatUint(uint64(reader.Fd()), 10))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup()
		_ = reader.Close()
	}()
	if supervisorLifetimeAlreadyLost(lost) {
		t.Fatal("lifetime was lost while writer remained open")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("writer EOF was not observed")
	}
}

func TestSupervisorLifetimeRejectsInvalidHandle(t *testing.T) {
	if _, _, err := startSupervisorLifetime("not-a-handle"); err == nil {
		t.Fatal("invalid handle accepted")
	}
}
