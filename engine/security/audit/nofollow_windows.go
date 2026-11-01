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

package audit

import (
	"fmt"
	"os"
)

// noFollow has no windows equivalent for os.OpenFile.
const noFollow = 0

// refuseLink refuses an existing symbolic link or junction (reparse point) at the
// journal path, since the open below would follow it.
func refuseLink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return nil // absent: the open creates a regular file
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("audit journal path is a link")
	}
	return nil
}
