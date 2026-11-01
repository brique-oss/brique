//go:build windows

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

package execution

import (
	"os"
	"os/exec"
	"strconv"
)

// setWrapperProcGroup is a no-op on Windows: the tree is killed with taskkill.
func setWrapperProcGroup(cmd *exec.Cmd) {}

// killWrapperGroup kills pid and its children.
func killWrapperGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	if p, err := os.FindProcess(pid); err == nil && p != nil {
		_ = p.Kill()
	}
}
