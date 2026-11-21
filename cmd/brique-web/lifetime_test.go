/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSupervisorLifetimeReportsEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lost, closeLifetime, err := startSupervisorLifetime(fmt.Sprint(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer closeLifetime()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("supervisor lifetime EOF was not reported")
	}
}
