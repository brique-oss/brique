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

package shared

// -----------------------------
// Lifecycle
// -----------------------------

type ContextState int

const (
	ContextInitializing ContextState = iota
	ContextRunning
	ContextStopping
	ContextStopped
	ContextFailed
)

type FamilyState int

const (
	FamilyInitializing FamilyState = iota
	FamilyRunning
	FamilyStopping
	FamilyStopped
	FamilyFailed
)

func (s FamilyState) String() string {
	switch s {
	case FamilyInitializing:
		return "initializing"
	case FamilyRunning:
		return "running"
	case FamilyStopping:
		return "stopping"
	case FamilyStopped:
		return "stopped"
	case FamilyFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type ControlKind int

const (
	CtrlSystem ControlKind = iota
	CtrlStop
	CtrlRestart
)

type ControlSignal struct {
	Kind ControlKind
	Data map[string]any
}

type ContextAddr string

type FamilyName string

const (
	FamilyComm      FamilyName = "communication"
	FamilyExecution FamilyName = "execution"
	FamilyMatter    FamilyName = "matter"
	FamilyTrace     FamilyName = "trace"
	FamilyReflexive FamilyName = "reflexive"
)

// RootContextID is the canonical internal absolute context id of the instance boundary context.
// convention: all internal context ids are absolute and start with '/'.
const RootContextID = "/root"
