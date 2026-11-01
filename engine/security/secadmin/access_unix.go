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

package secadmin

import "os"

// accessProblem reports an entry of the security directory that group or others can
// reach, or "" when it is acceptable.
func accessProblem(_, rel string, info os.FileInfo) string {
	switch {
	case info.IsDir() && info.Mode().Perm()&0o077 != 0:
		return fmtProblem("%s is accessible by group or others (mode %o); expected 0700", rel, info.Mode().Perm())
	case info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0:
		return fmtProblem("%s is accessible by group or others (mode %o); expected 0600 or 0400", rel, info.Mode().Perm())
	}
	return ""
}
