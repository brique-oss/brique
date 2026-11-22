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

// Association is what the owner chose for one browser extension: the instances it vouches for
// (a page served by one of them is admitted as local.ui) and the one instance that carries the
// person's identity, which signs when the page belongs to an instance that is not local.
type Association struct {
	Revision  uint64
	Instances []Selection
	// Identity is the alias of the instance that signs for pages of other instances.
	Identity string
}

// Instance returns the selection of an associated instance by alias.
func (association Association) Instance(alias string) (Selection, bool) {
	for _, selection := range association.Instances {
		if selection.Alias == alias {
			return selection, true
		}
	}
	return Selection{}, false
}
