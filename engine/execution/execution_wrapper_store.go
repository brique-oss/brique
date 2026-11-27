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

package execution

// execution_wrapper_store.go
//
// The private directory of a wrapper. A wrapper that declares `run.store` gets, at launch, a directory
// of its own under the machine's protected directory (<machine>/wrappers/<wrapper>, owner-only,
// outside the instance) and its path in BRIQUE_WRAPPER_STORE. It is for state that changes while the
// wrapper runs and must not live in the instance or in a Matter (tokens it refreshes, files the
// operator installs for it). The wrapper is given that path only, never the machine directory, which
// holds the keys of the instance; the variable that names the machine directory is removed from every
// wrapper's environment. The same operating-system account can still read the machine directory: this
// narrows what a wrapper is told, not what it could find (SECURITY.md §1.4).

import (
	"path/filepath"
	"strings"

	"brique_engine/engine/junction"
	"brique_engine/engine/security/hostdir"
	"brique_engine/engine/shared"
	"brique_engine/platform/protectedfile"
)

const (
	// wrapperStoreVariable is the variable that carries the path of the private directory.
	wrapperStoreVariable = "BRIQUE_WRAPPER_STORE"
)

// wrapperStoreEnv creates the private directory of a wrapper that declares one and returns its variable.
// With no machine directory the launch is refused, and the refusal is not remembered (like a secret):
// configuring the machine directory is enough to mend it.
func (l *ExecutionLoop) wrapperStoreEnv(cfg WrapperRuntimeCfg) (map[string]string, error) {
	if !cfg.Run.Store {
		return nil, nil
	}
	refuse := func(reason string) error { return &wrapperStoreError{wrapper: cfg.Name, reason: reason} }
	if !secretNamePattern.MatchString(cfg.Name) {
		return nil, refuse("the wrapper name cannot name a directory ([a-z][a-z0-9._-]{0,63})")
	}
	machine := ""
	if provider, ok := l.frame.CtxCommReg.(junction.MachineDirectoryProvider); ok {
		machine = provider.MachineDirectory()
	}
	if machine == "" {
		return nil, refuse("this instance has no machine security directory (brique-sec init)")
	}
	folder := filepath.Join(machine, shared.WrappersDir)
	directory := filepath.Join(folder, cfg.Name)
	for _, dir := range []string{folder, directory} {
		if err := protectedfile.CreateProtectedDirectory(dir); err != nil {
			return nil, refuse("cannot prepare the directory: " + err.Error())
		}
	}
	return map[string]string{wrapperStoreVariable: directory}, nil
}

// withoutMachineDirectory removes the variable that names the machine's directory from an environment.
func withoutMachineDirectory(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, hostdir.Environment+"=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// wrapperStoreError is a launch refused for the private store. Like a secret it is not remembered by the
// wrapper state (it matches errWrapperSecret), so configuring the machine directory is enough to mend it.
type wrapperStoreError struct{ wrapper, reason string }

func (e *wrapperStoreError) Error() string {
	return "wrapper " + e.wrapper + ": private store: " + e.reason
}

func (e *wrapperStoreError) Is(target error) bool { return target == errWrapperSecret }
