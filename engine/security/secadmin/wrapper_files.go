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

// wrapper_files.go
//
// Files the operator installs into the private directory of a wrapper (<machine>/wrappers/<wrapper>, the
// directory the engine gives a wrapper that declares run.store). They are configuration that must not live
// in the instance, where any principal that may write it could change it: a registry of the servers a wrapper
// may reach, the hashes of the tokens it accepts. The command knows nothing of what a file means: it checks
// that it is JSON within a bound and writes it atomically, owner-only. The wrapper validates the content and
// refuses what it does not understand.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"

	"brique_engine/engine/security/policysrc"
	"brique_engine/engine/shared"
)

// MaxWrapperFileBytes bounds one installed file.
const MaxWrapperFileBytes = 1 << 20

func wrapperFilePath(wrapper, name string) (string, error) {
	if !policysrc.ValidSecretName(wrapper) {
		return "", fmt.Errorf("invalid wrapper name %q: expected [a-z][a-z0-9._-]{0,63}", wrapper)
	}
	if !policysrc.ValidSecretName(name) {
		return "", fmt.Errorf("invalid file name %q: expected [a-z][a-z0-9._-]{0,63}", name)
	}
	return path.Join(shared.WrappersDir, wrapper, name), nil
}

// WrapperFileInstall replaces the file of a wrapper with content, which must be JSON.
func (d *Dir) WrapperFileInstall(wrapper, name string, content []byte) error {
	rel, err := wrapperFilePath(wrapper, name)
	if err != nil {
		return err
	}
	switch {
	case len(content) == 0:
		return errors.New("the file is empty")
	case len(content) > MaxWrapperFileBytes:
		return fmt.Errorf("the file is above %d bytes", MaxWrapperFileBytes)
	case !json.Valid(content):
		return errors.New("the file is not valid JSON")
	}
	if err := d.writeFile(rel, content, 0o600); err != nil {
		return fmt.Errorf("wrapper %s, file %s: %w", wrapper, name, err)
	}
	return nil
}

// WrapperFileShow returns the content of an installed file.
func (d *Dir) WrapperFileShow(wrapper, name string) ([]byte, error) {
	rel, err := wrapperFilePath(wrapper, name)
	if err != nil {
		return nil, err
	}
	content, err := d.readProtected(rel, MaxWrapperFileBytes)
	if err != nil {
		return nil, fmt.Errorf("wrapper %s, file %s: %w", wrapper, name, err)
	}
	return content, nil
}

// WrapperFileRemove deletes an installed file. Removing what is not there is an error.
func (d *Dir) WrapperFileRemove(wrapper, name string) error {
	rel, err := wrapperFilePath(wrapper, name)
	if err != nil {
		return err
	}
	if err := os.Remove(d.abs(rel)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("wrapper %s has no file %s", wrapper, name)
		}
		return fmt.Errorf("wrapper %s, file %s cannot be removed", wrapper, name)
	}
	return nil
}

// WrapperFileList lists the files of a wrapper, sorted. A wrapper with no directory yet has none.
func (d *Dir) WrapperFileList(wrapper string) ([]string, error) {
	if !policysrc.ValidSecretName(wrapper) {
		return nil, fmt.Errorf("invalid wrapper name %q", wrapper)
	}
	entries, err := os.ReadDir(d.abs(path.Join(shared.WrappersDir, wrapper)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && policysrc.ValidSecretName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
