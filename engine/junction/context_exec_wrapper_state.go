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

package junction

import (
	"sync"
	"time"
)

// -----------------------------
// Wrapper runtime state
// -----------------------------

type WrapperProcState string

const (
	ProcUnknown  WrapperProcState = "unknown"
	ProcStarting WrapperProcState = "starting"
	ProcRunning  WrapperProcState = "running"
	ProcStopping WrapperProcState = "stopping"
	ProcExited   WrapperProcState = "exited"
)

type WrapperState struct {
	mu sync.Mutex
	// Build state (compiled wrappers only)
	BuiltAt        time.Time
	RunningBuiltAt time.Time

	// Process state
	PID        int
	ProcState  WrapperProcState
	StartedAt  time.Time
	LastExitAt time.Time
	LastError  error

	// readiness
	Ready    bool
	ReadyAt  time.Time
	ReadyErr error
	readyCh  chan struct{}

	// in-flight guards
	building bool
	starting bool

	// Stop tracking
	StopRequestedAt time.Time
}

// Lock
//
// Functional role (Brique DSL):
// - acquire wrapper state mutex.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires `w.mu`.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Blocks until the mutex is acquired.

func (w *WrapperState) Lock() {
	w.mu.Lock()
}

// Unlock
//
// Functional role (Brique DSL):
// - release wrapper state mutex.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - releases `w.mu`.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Caller must already hold the mutex.

func (w *WrapperState) Unlock() {
	w.mu.Unlock()
}

// GetReadyCh
//
// Functional role (Brique DSL):
// - return current wrapper ready channel reference.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - returns chan struct{}.
//
// Contract:
// - Returns the stored channel reference, which may be nil.

func (w *WrapperState) GetReadyCh() chan struct{} {
	return w.readyCh
}

// SetReadyCh
//
// Functional role (Brique DSL):
// - allocate and store a new wrapper ready channel.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - replaces `w.readyCh` with a newly allocated channel.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Existing ready channel reference is overwritten without closing it.
// - Callers are responsible for synchronization; this helper does not lock.

func (w *WrapperState) SetReadyCh() {
	w.readyCh = make(chan struct{})
}

// ResetReadyCh
//
// Functional role (Brique DSL):
// - clear wrapper ready channel reference.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - sets `w.readyCh` to nil.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Does not close the previous ready channel.
// - Callers are responsible for synchronization; this helper does not lock.

func (w *WrapperState) ResetReadyCh() {
	w.readyCh = nil
}

// GetBuilding
//
// Functional role (Brique DSL):
// - return current wrapper build-in-progress flag.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - none.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Returns the stored flag value.
// - Caller synchronization is required; this accessor does not lock.

func (w *WrapperState) GetBuilding() bool {
	return w.building
}

// SetBuilding
//
// Functional role (Brique DSL):
// - set wrapper build-in-progress flag.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - updates `w.building`.
//
// Inputs:
// - receiver `w *WrapperState`.
// - building bool.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Overwrites the previous flag value.
// - Caller synchronization is required; this mutator does not lock.

func (w *WrapperState) SetBuilding(building bool) {
	w.building = building
}

// GetStarting
//
// Functional role (Brique DSL):
// - return current wrapper starting flag.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `w *WrapperState`.
//
// Outputs:
// - returns bool.
//
// Contract:
// - Returns the stored flag value.
// - Caller synchronization is required; this accessor does not lock.

func (w *WrapperState) GetStarting() bool {
	return w.starting
}

// SetStarting
//
// Functional role (Brique DSL):
// - set wrapper starting flag.
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
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - updates `w.starting`.
//
// Inputs:
// - receiver `w *WrapperState`.
// - starting bool.
//
// Outputs:
// - no direct return value; observable outputs are side effects defined in this function.
//
// Contract:
// - Overwrites the previous flag value.
// - Caller synchronization is required; this mutator does not lock.

func (w *WrapperState) SetStarting(starting bool) {
	w.starting = starting
}
