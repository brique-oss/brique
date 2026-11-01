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

package secadmin

import (
	"os"

	"brique_engine/shared"
)

// accessProblem reports an entry of the security directory whose owner or access
// list lets another principal in (windows has no mode bits: the access list is the
// permission), or "" when it is acceptable.
func accessProblem(absolute, rel string, info os.FileInfo) string {
	if !info.IsDir() && !info.Mode().IsRegular() {
		return ""
	}
	if err := shared.InspectProtectedPath(absolute, info.IsDir()); err != nil {
		return fmtProblem("%s: %v; expected an owner and an access list limited to the current user, the administrators and the system", rel, err)
	}
	return ""
}
