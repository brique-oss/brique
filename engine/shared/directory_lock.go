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
	"errors"
	"strings"
)

// DirectoryObjectID names one filesystem object, not one spelling of its path.
// Volume and File are opaque lowercase hexadecimal identifiers supplied by the
// platform implementation. The format is also the input to local discovery.
type DirectoryObjectID struct {
	Format   string `json:"format"`
	Platform string `json:"platform"`
	Volume   string `json:"volume"`
	File     string `json:"file"`
}

const DirectoryObjectIDFormat = "brique.directory-object/1"

func (id DirectoryObjectID) Valid() bool {
	return id.Format == DirectoryObjectIDFormat && id.Platform != "" && validHexIdentifier(id.Volume) && validHexIdentifier(id.File)
}

func (id DirectoryObjectID) Key() string {
	if !id.Valid() {
		return ""
	}
	return id.Format + ":" + id.Platform + ":" + id.Volume + ":" + id.File
}

func validHexIdentifier(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

var ErrDirectoryLocked = errors.New("directory object is already owned by another engine")

// DirectoryLock is held for the whole engine lifetime. Closing it releases the
// kernel primitive; it never removes or writes anything inside the directory.
type DirectoryLock interface {
	Identity() DirectoryObjectID
	Close() error
}
