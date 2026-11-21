/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package instanceselection carries, without behaviour, what the browser native
// host needs to know about the instance the launcher selected for the browser.
package instanceselection

import "brique_engine/contracts/directoryobject"

// Selection is the instance chosen as browser gateway.
type Selection struct {
	Alias    string
	Revision uint64
	// RootObject and RootPath identify the instance root; the host compares them
	// with the engine descriptor.
	RootObject directoryobject.ID
	RootPath   string
	// HasPublication reports whether the instance has a web publication, which is
	// what makes the supervised web origin the local browser origin.
	HasPublication bool
}

// WebStatus is the supervised web host status of one instance.
type WebStatus struct {
	Alias  string
	Ready  bool
	WebPID int
	WebURL string
}
