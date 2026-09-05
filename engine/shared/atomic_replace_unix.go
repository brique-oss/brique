/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//go:build !windows

package shared

import (
	"fmt"
	"os"
)

// AtomicReplace
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate non-empty temporary and destination paths
//   - atomically replace destination file with temporary file using POSIX rename semantics
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - may replace the file at `dst` with the file currently at `tmp` through `os.Rename`.
//
// Inputs:
// - tmp string, dst string.
//
// Outputs:
// - returns error.
//
// Contract:
// - Returns exactly one `error`.
// - Emits no response, trace, or outbound message.
// - Returns an error when `tmp` or `dst` is empty.
// - Otherwise delegates the replace operation to `os.Rename`, relying on same-filesystem atomic rename semantics.
// - On rename failure, cleanup of `tmp` is the caller's responsibility.
func AtomicReplace(tmp, dst string) error {
	if tmp == "" || dst == "" {
		return fmt.Errorf("atomicReplace: empty path")
	}
	// On POSIX, rename is atomic (same filesystem).
	return os.Rename(tmp, dst)
}
