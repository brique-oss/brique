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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Files that travel with the instance — the signed policy, the access source — are
// ordinary files: a version control system gives them ordinary permissions and whoever
// checked them out owns them. Nothing here vouches for their content: a caller either
// verifies a signature over what it read or is an administration tool that is about to
// sign it. What these functions refuse is the way to the file leaving its root.

// ReadInstanceFile reads one regular file below root. The path must be a canonical
// relative path; a symbolic link anywhere on the way, a file that is not regular and one
// larger than maxBytes are refused.
func ReadInstanceFile(root, relative string, maxBytes int64) ([]byte, error) {
	if root == "" || maxBytes <= 0 || !ValidProtectedRelativePath(relative) {
		return nil, errors.New("invalid instance file path")
	}
	current := filepath.Clean(root)
	for _, segment := range strings.Split(relative, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("instance file path crosses a symbolic link")
		}
	}
	info, err := os.Lstat(current)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("instance file is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("instance file is larger than %d bytes", maxBytes)
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("instance file is larger than %d bytes", maxBytes)
	}
	return content, nil
}

// WriteInstanceFile atomically replaces one file below root, creating its directories
// with ordinary permissions. It refuses to write through a symbolic link.
func WriteInstanceFile(root, relative string, content []byte, mode os.FileMode) error {
	if root == "" || !ValidProtectedRelativePath(relative) {
		return errors.New("invalid instance file path")
	}
	current := filepath.Clean(root)
	segments := strings.Split(relative, "/")
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return errors.New("instance file path crosses a symbolic link")
		case err == nil && index < len(segments)-1 && !info.IsDir():
			return errors.New("instance file path crosses a file")
		case errors.Is(err, os.ErrNotExist) && index < len(segments)-1:
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	return WriteFileAtomic(current, content, mode)
}
