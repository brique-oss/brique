//go:build unix

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

// Package accesstest lets tests make a path unsafe the way the platform defines it:
// reachable by group or others on unix, by Everyone on windows. It is for tests only.
package accesstest

import (
	"os"
	"testing"
)

// Loosen lets group and others (unix) or Everyone (windows) into a path.
func Loosen(t testing.TB, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
}
