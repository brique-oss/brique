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

// Package hostdir says where the two parts of an instance's security live and how a
// program finds them from the instance alone.
//
// The part that travels with the instance is the "security" folder of the instance — the
// directory that holds the root context directory next to its wrappers — and not a folder
// inside the root context: nothing under the root context can name it. It holds the access
// source (who may do what) and, for an instance that carries signed policies, those too. The part that belongs to one machine — private keys, the trust
// anchor, the signed policies unless the instance carries them, durable state, the denial
// journal — is a protected directory outside the instance, by default one per instance name
// under the user's own directory.
package hostdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"brique_engine/shared"
)

const (
	// PolicyFolder is the folder of an instance that carries its signed policy.
	PolicyFolder = "security"
	// AccessFile is the access source inside PolicyFolder.
	AccessFile = "access.json"
	// Environment names the machine's directory when no flag does.
	Environment = "BRIQUE_SECURITY_DIR"

	// TemplateInstanceID is the name the instance template ships with. It is reserved: an
	// access source that still carries it has not been given a name by its owner, so nothing
	// is configured for it, and two instances made from the template can never share a
	// directory on a machine.
	TemplateInstanceID = "my-brique-instance"

	maxAccessBytes = 4 << 20
)

var instanceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// InstanceDirectory is the directory that holds the root context directory: the instance
// itself, next to its wrappers.
func InstanceDirectory(rootPath string) string { return filepath.Dir(filepath.Clean(rootPath)) }

// PolicyDirectory is the security folder of the instance whose root context directory is
// rootPath: a sibling of it, outside the tree of contexts.
func PolicyDirectory(rootPath string) string {
	return filepath.Join(InstanceDirectory(rootPath), PolicyFolder)
}

// CarriesGenerations reports whether the instance carries signed policies of its own, to be
// verified against an authority the machine was told to trust.
func CarriesGenerations(rootPath string) bool {
	info, err := os.Lstat(filepath.Join(PolicyDirectory(rootPath), "generations"))
	return err == nil && info.IsDir()
}

// HasPolicy reports whether the instance carries an access source.
func HasPolicy(rootPath string) bool {
	info, err := os.Lstat(filepath.Join(PolicyDirectory(rootPath), AccessFile))
	return err == nil && info.Mode().IsRegular()
}

// InstanceID reads the name of the instance from its access source.
func InstanceID(rootPath string) (string, error) {
	content, err := shared.ReadInstanceFile(PolicyDirectory(rootPath), AccessFile, maxAccessBytes)
	if err != nil {
		return "", fmt.Errorf("the instance has no readable %s/%s: %w", PolicyFolder, AccessFile, err)
	}
	var header struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal(content, &header); err != nil || !instanceIDPattern.MatchString(header.InstanceID) {
		return "", errors.New("the access source of the instance does not name a valid instance")
	}
	return header.InstanceID, nil
}

// Default is the machine's directory for an instance when nothing says otherwise:
// ~/.brique/<instance> (%APPDATA%\Brique\<instance> on windows).
func Default(instanceID string) (string, error) {
	if !instanceIDPattern.MatchString(instanceID) {
		return "", fmt.Errorf("%q is not a valid instance name", instanceID)
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(base, "Brique", instanceID), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("the user's home directory is unknown")
	}
	return filepath.Join(home, ".brique", instanceID), nil
}

// Locations are the two directories a program works with.
type Locations struct {
	// Machine is the machine's protected directory; empty means security is not configured.
	Machine string
	// Policy is the instance's security folder when it carries signed policies; empty when
	// the machine keeps them (the instance may still carry the access source).
	Policy string
}

// Resolve finds both directories for an instance. An explicit machine directory (flag,
// then BRIQUE_SECURITY_DIR) always wins. An instance that carries a security folder falls
// back to the default directory for its name. An instance without one and without an
// explicit directory has no security configured: the environment alone never switches
// security on for it. The instance's signed policies are read from it only if it carries
// them; otherwise the machine's directory holds them.
func Resolve(rootPath, flagValue string) (Locations, error) {
	var locations Locations
	hasSource := rootPath != "" && HasPolicy(rootPath)
	if hasSource && CarriesGenerations(rootPath) {
		locations.Policy = PolicyDirectory(rootPath)
	}
	switch {
	case flagValue != "":
		locations.Machine = flagValue
	case hasSource && os.Getenv(Environment) != "":
		locations.Machine = os.Getenv(Environment)
	case hasSource:
		id, err := InstanceID(rootPath)
		if err != nil {
			return Locations{}, err
		}
		if id == TemplateInstanceID {
			// Made from the template and never initialized: no security is configured.
			return Locations{Policy: locations.Policy}, nil
		}
		if locations.Machine, err = Default(id); err != nil {
			return Locations{}, err
		}
	}
	if locations.Machine != "" {
		absolute, err := filepath.Abs(locations.Machine)
		if err != nil {
			return Locations{}, err
		}
		locations.Machine = absolute
	}
	return locations, nil
}
