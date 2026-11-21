/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"errors"
	"os"
	"strconv"
	"sync"
)

// startSupervisorLifetime watches an inherited pipe/handle. The protocol has
// no messages: only EOF is meaningful, so a hard launcher exit cannot leave a
// launcher-owned engine running indefinitely.
func startSupervisorLifetime(raw string) (<-chan struct{}, func(), error) {
	if raw == "" {
		return nil, func() {}, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return nil, nil, errors.New("invalid supervisor lifetime handle")
	}
	file := os.NewFile(uintptr(value), "brique supervisor lifetime")
	if file == nil {
		return nil, nil, errors.New("supervisor lifetime handle unavailable")
	}
	lost := make(chan struct{})
	var closeOnce sync.Once
	closeFile := func() { closeOnce.Do(func() { _ = file.Close() }) }
	go func() {
		buffer := make([]byte, 1)
		for {
			if _, err := file.Read(buffer); err != nil {
				close(lost)
				return
			}
		}
	}()
	return lost, closeFile, nil
}

func supervisorLifetimeAlreadyLost(lost <-chan struct{}) bool {
	if lost == nil {
		return false
	}
	select {
	case <-lost:
		return true
	default:
		return false
	}
}
