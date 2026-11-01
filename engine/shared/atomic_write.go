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
	"io"
	"os"
	"path/filepath"
)

// SyncDir flushes directory metadata (a rename or creation) to stable storage.
// It is best effort: some platforms cannot sync a directory.
func SyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// SyncFile flushes an existing file's contents to stable storage.
func SyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteFileSynced writes data to path (not atomically) and flushes it. Use it
// for staging files that a later AtomicReplace publishes.
func WriteFileSynced(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteFileAtomic replaces path with data so that readers see either the old
// or the new content, even across a crash: the data goes to a uniquely named
// temporary file in the same directory, is flushed, renamed over the target,
// and the directory is flushed. Concurrent writers never share a temp file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeAtomicFrom(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// CopyFileAtomic copies src over dst with the guarantees of WriteFileAtomic.
func CopyFileAtomic(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomicFrom(dst, perm, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

func writeAtomicFrom(path string, perm os.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := fill(tmp); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := AtomicReplace(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	SyncDir(dir)
	return nil
}
