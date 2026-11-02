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

package comm

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// certificateRecheckInterval bounds how often the certificate files are looked at.
const certificateRecheckInterval = 2 * time.Second

// reloadingCertificate serves the TLS certificate of an interface and picks up a new one
// when its files change, so that renewing or regenerating the certificate needs no restart.
// A new pair that cannot be loaded (half written, mismatched) is ignored and the last good one
// keeps being served.
type reloadingCertificate struct {
	certFile, keyFile string

	mu        sync.Mutex
	current   *tls.Certificate
	loadedAt  time.Time
	checkedAt time.Time
	now       func() time.Time
	stamp     [2]time.Time
}

func newReloadingCertificate(certFile, keyFile string) (*reloadingCertificate, error) {
	r := &reloadingCertificate{certFile: certFile, keyFile: keyFile, now: time.Now}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func modTime(path string) time.Time {
	if info, err := os.Stat(filepath.Clean(path)); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func (r *reloadingCertificate) load() error {
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.current = &pair
	r.stamp = [2]time.Time{modTime(r.certFile), modTime(r.keyFile)}
	r.loadedAt = r.now()
	return nil
}

func (r *reloadingCertificate) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.Sub(r.checkedAt) >= certificateRecheckInterval {
		r.checkedAt = now
		if stamp := [2]time.Time{modTime(r.certFile), modTime(r.keyFile)}; stamp != r.stamp {
			previous := r.current
			if err := r.load(); err != nil {
				// Keep serving the last good pair; look again when the files change once more.
				r.current, r.stamp = previous, stamp
			}
		}
	}
	return r.current, nil
}
