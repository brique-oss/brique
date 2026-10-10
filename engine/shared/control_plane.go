/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import "fmt"

// DefaultControlMessageBytes is the default maximum serialized size of an
// intention or response transported through a communication interface. Large
// business payloads belong in Matter substance and travel through its data
// plane instead of the intention control plane.
const DefaultControlMessageBytes int64 = 4 << 20

// MaxInlineTraceMessageBytes bounds the full control message retained by a
// communication trace. Larger messages are represented by routing metadata,
// serialized size, a digest, and their top-level payload keys.
const MaxInlineTraceMessageBytes = 256 << 10

// ValidateControlMessageBytes applies the configured limit, falling back to
// DefaultControlMessageBytes. It deliberately points callers to Matter, the
// engine's bulk-data plane.
func ValidateControlMessageBytes(size, limit int64) error {
	limit = EffectiveControlMessageLimit(limit)
	if size <= limit {
		return nil
	}
	return fmt.Errorf("control message is %d bytes, limit is %d; store large payloads in Matter substance", size, limit)
}

// EffectiveControlMessageLimit permits interface configuration to lower, but
// never raise, the global control-plane ceiling.
func EffectiveControlMessageLimit(configured int64) int64 {
	if configured <= 0 || configured > DefaultControlMessageBytes {
		return DefaultControlMessageBytes
	}
	return configured
}
