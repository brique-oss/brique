/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"os"
	"sync"
)

type supervisorLifetime struct {
	child     *os.File
	keepalive *os.File
	mu        sync.Mutex
}

func (lifetime *supervisorLifetime) afterStart() error {
	if lifetime == nil {
		return nil
	}
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if lifetime.child == nil {
		return nil
	}
	err := lifetime.child.Close()
	lifetime.child = nil
	return err
}

func (lifetime *supervisorLifetime) close() error {
	if lifetime == nil {
		return nil
	}
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	var result error
	if lifetime.child != nil {
		result = lifetime.child.Close()
		lifetime.child = nil
	}
	if lifetime.keepalive != nil {
		if err := lifetime.keepalive.Close(); result == nil {
			result = err
		}
		lifetime.keepalive = nil
	}
	return result
}

func (lifetime *supervisorLifetime) lose() error {
	if lifetime == nil {
		return nil
	}
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if lifetime.keepalive == nil {
		return nil
	}
	err := lifetime.keepalive.Close()
	lifetime.keepalive = nil
	return err
}
