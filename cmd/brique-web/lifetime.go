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

// startSupervisorLifetime uses the same EOF-only inherited-handle contract as
// the engine. A launcher crash therefore cannot leave its web host orphaned.
func startSupervisorLifetime(raw string) (<-chan struct{}, func(), error) {
	if raw == "" {
		return nil, func() {}, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return nil, nil, errors.New("invalid supervisor lifetime handle")
	}
	file := os.NewFile(uintptr(value), "brique web supervisor lifetime")
	if file == nil {
		return nil, nil, errors.New("supervisor lifetime handle unavailable")
	}
	lost := make(chan struct{})
	var once sync.Once
	closeFile := func() { once.Do(func() { _ = file.Close() }) }
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
