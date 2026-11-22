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

// secrets.go
//
// The values of the secrets that wrappers may be given. They belong to the machine, never
// to the instance: one owner-only file each, under SecretsDir in the machine's part of the
// security directory, written and read with the protected-file functions that already
// guard the keys. Who may receive a value is not decided here: it is a right of the access
// source, compiled into the signed policy.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"brique_engine/engine/security/policysrc"
	"brique_engine/engine/shared"
)

const (
	// SecretsDir is the folder of the values, relative to the machine's directory. The engine
	// reads the same folder at the launch of a wrapper (shared.SecretsDir).
	SecretsDir = shared.SecretsDir
	// MaxSecretBytes bounds one value.
	MaxSecretBytes = shared.MaxSecretBytes

	secretMode = 0o600
)

// SecretInfo describes a stored secret. It never carries the value.
type SecretInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

func checkSecretName(name string) error {
	if !policysrc.ValidSecretName(name) {
		return fmt.Errorf("invalid secret name %q: expected [a-z][a-z0-9._-]{0,63}", name)
	}
	return nil
}

// SecretAdd stores a value, replacing the one the name already has: that is how a secret
// is rotated. The line ending that ends a value typed or piped in is not part of it. The
// value is never part of an error.
func (d *Dir) SecretAdd(name string, value []byte) (size int, err error) {
	if err := checkSecretName(name); err != nil {
		return 0, err
	}
	value = bytes.TrimRight(value, "\r\n")
	switch {
	case len(value) == 0:
		return 0, errors.New("a secret value cannot be empty")
	case len(value) > MaxSecretBytes:
		return 0, fmt.Errorf("a secret value is at most %d bytes", MaxSecretBytes)
	case bytes.IndexByte(value, 0) >= 0:
		return 0, errors.New("a secret value cannot contain a NUL byte")
	}
	if err := d.writeFile(path.Join(SecretsDir, name), value, secretMode); err != nil {
		return 0, fmt.Errorf("secret %s: %w", name, err)
	}
	return len(value), nil
}

// SecretRemove deletes a value. The right to receive it, kept in the access source, is not
// touched.
func (d *Dir) SecretRemove(name string) error {
	if err := checkSecretName(name); err != nil {
		return err
	}
	if err := os.Remove(d.abs(path.Join(SecretsDir, name))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secret %s does not exist", name)
		}
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			err = pathError.Err
		}
		return fmt.Errorf("secret %s cannot be removed: %w", name, err)
	}
	return nil
}

// SecretList lists the stored secrets by name. A folder that does not exist yet holds none.
func (d *Dir) SecretList() ([]SecretInfo, error) {
	entries, err := os.ReadDir(d.abs(SecretsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("the secrets are unavailable")
	}
	var out []SecretInfo
	for _, entry := range entries {
		// A leftover temporary file (".tmp-…") or anything that is not a secret does not
		// match the name pattern and is not listed.
		if !entry.Type().IsRegular() || !policysrc.ValidSecretName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, SecretInfo{Name: entry.Name(), Size: info.Size(), ModTime: info.ModTime().UTC()})
	}
	return out, nil
}

// SecretAllow gives a wrapper the right to receive a secret (the `secrets` section of the access source, signed by apply). It says nothing
// about the value: that is what SecretAdd stores. Allowing twice is not an error.
func (d *Dir) SecretAllow(name, wrapper string) error {
	if !policysrc.ValidSecretName(name) {
		return fmt.Errorf("secret name %q: expected a name like [a-z][a-z0-9._-]{0,63}", name)
	}
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	if src.Secrets == nil {
		src.Secrets = map[string]*policysrc.SecretGrant{}
	}
	grant := src.Secrets[name]
	if grant == nil {
		grant = &policysrc.SecretGrant{}
		src.Secrets[name] = grant
	}
	for _, existing := range grant.Wrappers {
		if existing == wrapper {
			return nil
		}
	}
	grant.Wrappers = append(grant.Wrappers, wrapper)
	return d.SaveSource(src)
}

// SecretDeny withdraws that right; the section of a secret that no wrapper may receive any more is removed. Denying what was not allowed is an
// error, so a typo does not look like a success.
func (d *Dir) SecretDeny(name, wrapper string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	grant := src.Secrets[name]
	if grant == nil {
		return fmt.Errorf("no wrapper may receive secret %q", name)
	}
	kept := grant.Wrappers[:0:0]
	for _, existing := range grant.Wrappers {
		if existing != wrapper {
			kept = append(kept, existing)
		}
	}
	if len(kept) == len(grant.Wrappers) {
		return fmt.Errorf("wrapper %q may not receive secret %q", wrapper, name)
	}
	if len(kept) == 0 {
		delete(src.Secrets, name)
	} else {
		grant.Wrappers = kept
	}
	return d.SaveSource(src)
}
