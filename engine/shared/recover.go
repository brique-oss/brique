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

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Recover is deferred at the top of every goroutine that serves one request, and
// inside every loop iteration of a long-lived loop:
//
//	defer shared.Recover(func(text string) { ... })
//
// A panic that nothing recovers stops the whole process, so one defect reached by
// one request would stop the engine for every context and every user. Recover
// turns the panic into a call to handle with a description for the trace; handle
// decides what the caller is told. It must itself not panic.
//
// recover only works when called directly by the deferred function: Recover has
// to be the deferred call, not be called from another deferred closure.
func Recover(handle func(text string)) {
	if r := recover(); r != nil {
		handle(PanicText(r, debug.Stack()))
	}
}

// PanicText describes a panic for a trace: its value and the first frames of the
// stack that raised it. It is for operators, never for the caller of a request.
func PanicText(value any, stack []byte) string {
	const frames = 8
	lines := strings.Split(strings.TrimSpace(string(stack)), "\n")
	// The stack starts with "goroutine N [running]:" and then pairs of lines
	// (function, file:line). Keep the function and location of the first frames.
	if len(lines) > 1+2*frames {
		lines = lines[:1+2*frames]
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return fmt.Sprintf("panic: %v | %s", value, strings.Join(lines, " ; "))
}
