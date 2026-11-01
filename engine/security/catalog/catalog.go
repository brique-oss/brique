/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package catalog builds the closed-world vocabulary accepted by trust policy
// compilation. Engine capacities are listed only with reviewed resource
// extractors; opaque user capacities are discovered from the instance tree and
// are authorized as capability units without transitive resource extraction.
package catalog

import (
	"brique_engine/security/trust"
)

// Build returns the vocabulary a trust policy is compiled against: the resource
// kinds and actions the engine understands. It names no capacity and reads nothing
// from the instance: an engine capacity is opened by the verbs of its class
// (security/access), a user capacity by the capacity/execute resource, so a
// malformed or newly installed capacity file can neither stop the engine from
// starting nor from reloading a policy. rootDirectory is kept so the callers do not
// change; it is not read.
func Build(rootDirectory string) (trust.Catalog, error) {
	return trust.Catalog{ResourceActions: ResourceActions()}, nil
}

// ResourceActions is the closed set of resource actions the engine understands,
// per kind. It does not depend on the instance, so offline tools can use it.
func ResourceActions() map[string][]string { return trust.EngineResourceActions() }
