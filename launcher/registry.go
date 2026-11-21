/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package launcher implements the machine-local launcher registry. It contains
// no Brique identity, policy or intention handling.
package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/contracts/directoryobject"
	"brique_engine/contracts/publication"
	"brique_engine/contracts/webprofile"
	"brique_engine/platform/locking"
	"brique_engine/platform/protectedfile"
)

var publicationIDPattern = regexp.MustCompile(`^spub1_[A-Za-z0-9_-]{43}$`)

const (
	RegistryFormat         = "brique.launcher-registry/4"
	version3RegistryFormat = "brique.launcher-registry/3"
	previousRegistryFormat = "brique.launcher-registry/2"
	legacyRegistryFormat   = "brique.launcher-registry/1"
	SupervisionManaged     = "managed"
	SupervisionExternal    = "external"
	registryRelative       = "registry/registry.json"
	registryLock           = "registry/registry.lock"
	maxRegistryBytes       = 4 << 20
)

type Registry struct {
	Format    string     `json:"format"`
	Revision  uint64     `json:"revision"`
	Instances []Instance `json:"instances"`
}

type Instance struct {
	Alias             string              `json:"alias"`
	RootPath          string              `json:"root_path"`
	RootObject        directoryobject.ID  `json:"root_object"`
	SecurityDirectory string              `json:"security_directory,omitempty"`
	SecurityObject    *directoryobject.ID `json:"security_object,omitempty"`
	EnginePath        string              `json:"engine_path"`
	Build             buildinfo.Info      `json:"build"`
	RuntimePath       string              `json:"runtime_path,omitempty"`
	RuntimeID         string              `json:"runtime_id,omitempty"`
	PublicationPath   string              `json:"publication_path,omitempty"`
	PublicationID     string              `json:"publication_id,omitempty"`
	Supervision       string              `json:"supervision"`
	Autostart         bool                `json:"autostart,omitempty"`
	Web               *WebProfile         `json:"web,omitempty"`
}

const (
	WebProfileLocal  = "local"
	WebProfileServer = "server"
)

// WebProfile is the persisted public posture of the web host. An absent profile
// is the local profile with a dynamic loopback port. The server profile is the
// only one that names a public authority; it still listens on loopback only and
// is reached through the trusted reverse proxy.
type WebProfile struct {
	Mode           string   `json:"mode"`
	Address        string   `json:"address,omitempty"`
	AllowedHosts   []string `json:"allowed_hosts,omitempty"`
	PublicOrigins  []string `json:"public_origins,omitempty"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

type AddOptions struct {
	Alias, RootPath, SecurityDirectory, EnginePath, RuntimePath, PublicationPath, Supervision string
	Autostart                                                                                 bool
	Web                                                                                       *WebProfile
}

// RuntimeInspector is replaceable in tests; the production implementation
// executes the candidate binary directly, without a shell.
type RuntimeInspector func(context.Context, string) (buildinfo.Info, error)
type RuntimePackageInspector func(context.Context, string) (RuntimeInspection, error)
type PublicationInspector func(string) (publication.Inspection, error)

type Store struct {
	Directory          string
	Inspect            RuntimeInspector
	InspectPackage     RuntimePackageInspector
	InspectPublication PublicationInspector
}

func DefaultDataDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "brique-launcher"), nil
}

func (s Store) List() (Registry, error) {
	var result Registry
	err := s.withLock(func(registry *Registry) error {
		result = cloneRegistry(*registry)
		return nil
	})
	return result, err
}

func (s Store) Get(alias string) (Instance, error) {
	registry, err := s.List()
	if err != nil {
		return Instance{}, err
	}
	for _, instance := range registry.Instances {
		if instance.Alias == alias {
			return instance, nil
		}
	}
	return Instance{}, fmt.Errorf("instance %q is not registered", alias)
}

func (s Store) Add(ctx context.Context, options AddOptions) (Instance, error) {
	instance, err := s.resolve(ctx, options)
	if err != nil {
		return Instance{}, err
	}
	err = s.withLock(func(registry *Registry) error {
		for _, current := range registry.Instances {
			if current.Alias == instance.Alias {
				return fmt.Errorf("alias %q is already registered", instance.Alias)
			}
			if current.RootObject.Key() == instance.RootObject.Key() {
				return fmt.Errorf("root is already registered as %q", current.Alias)
			}
			if current.SecurityObject != nil && instance.SecurityObject != nil && current.SecurityObject.Key() == instance.SecurityObject.Key() {
				return fmt.Errorf("security directory is already registered as %q", current.Alias)
			}
			if webAddressTaken(current, instance.Web) {
				return fmt.Errorf("web host address is already used by %q", current.Alias)
			}
		}
		registry.Instances = append(registry.Instances, instance)
		registry.Revision++
		sort.Slice(registry.Instances, func(i, j int) bool { return registry.Instances[i].Alias < registry.Instances[j].Alias })
		return s.write(*registry)
	})
	return instance, err
}

// Remove deletes only the registry record. It never removes an instance,
// security directory, log or runtime file.
func (s Store) Remove(alias string) (Instance, error) {
	if !validAlias(alias) {
		return Instance{}, errors.New("invalid instance alias")
	}
	var removed Instance
	err := s.withLock(func(registry *Registry) error {
		for index, instance := range registry.Instances {
			if instance.Alias != alias {
				continue
			}
			removed = instance
			registry.Instances = append(registry.Instances[:index], registry.Instances[index+1:]...)
			registry.Revision++
			return s.write(*registry)
		}
		return fmt.Errorf("instance %q is not registered", alias)
	})
	return removed, err
}

func (s Store) UpdateEngine(ctx context.Context, alias, enginePath string) (Instance, error) {
	if !validAlias(alias) {
		return Instance{}, errors.New("invalid instance alias")
	}
	engine, err := resolveFile(enginePath)
	if err != nil {
		return Instance{}, fmt.Errorf("invalid engine binary: %w", err)
	}
	inspect := s.Inspect
	if inspect == nil {
		inspect = InspectRuntime
	}
	build, err := inspect(ctx, engine)
	if err != nil {
		return Instance{}, fmt.Errorf("engine build information unavailable: %w", err)
	}
	if !build.Valid() || build.OS != runtime.GOOS || build.Arch != runtime.GOARCH {
		return Instance{}, errors.New("engine build information is invalid or incompatible with this machine")
	}
	var updated Instance
	err = s.withLock(func(registry *Registry) error {
		for index := range registry.Instances {
			if registry.Instances[index].Alias != alias {
				continue
			}
			if registry.Instances[index].PublicationPath != "" {
				return errors.New("clear the web publication before selecting a standalone engine")
			}
			registry.Instances[index].EnginePath = engine
			registry.Instances[index].Build = build
			registry.Instances[index].RuntimePath = ""
			registry.Instances[index].RuntimeID = ""
			updated = registry.Instances[index]
			registry.Revision++
			return s.write(*registry)
		}
		return fmt.Errorf("instance %q is not registered", alias)
	})
	return updated, err
}

func (s Store) UpdateRuntime(ctx context.Context, alias, runtimePath string) (Instance, error) {
	if !validAlias(alias) {
		return Instance{}, errors.New("invalid instance alias")
	}
	current, err := s.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	runtimePath, runtimeID, engine, build, err := s.resolveRuntimePackage(ctx, runtimePath)
	if err != nil {
		return Instance{}, err
	}
	if current.PublicationPath != "" {
		if err := s.validateWebRuntime(ctx, runtimePath); err != nil {
			return Instance{}, err
		}
	}
	var updated Instance
	err = s.withLock(func(registry *Registry) error {
		for index := range registry.Instances {
			if registry.Instances[index].Alias != alias {
				continue
			}
			if registry.Instances[index].PublicationID != current.PublicationID || registry.Instances[index].PublicationPath != current.PublicationPath {
				return errors.New("instance publication changed during runtime update")
			}
			registry.Instances[index].EnginePath = engine
			registry.Instances[index].Build = build
			registry.Instances[index].RuntimePath = runtimePath
			registry.Instances[index].RuntimeID = runtimeID
			updated = registry.Instances[index]
			registry.Revision++
			return s.write(*registry)
		}
		return fmt.Errorf("instance %q is not registered", alias)
	})
	return updated, err
}

// UpdatePublication atomically associates a fully verified immutable snapshot.
// An empty path removes only the association; it never deletes publication files.
func (s Store) UpdatePublication(ctx context.Context, alias, publicationPath string) (Instance, error) {
	if !validAlias(alias) {
		return Instance{}, errors.New("invalid instance alias")
	}
	path, id, err := s.resolvePublication(publicationPath)
	if err != nil {
		return Instance{}, err
	}
	current, err := s.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	if path != "" {
		if current.RuntimePath == "" {
			return Instance{}, errors.New("web publication requires a packaged runtime")
		}
		if err := s.validateWebRuntime(ctx, current.RuntimePath); err != nil {
			return Instance{}, err
		}
	}
	var updated Instance
	err = s.withLock(func(registry *Registry) error {
		for index := range registry.Instances {
			if registry.Instances[index].Alias != alias {
				continue
			}
			if registry.Instances[index].RuntimeID != current.RuntimeID || registry.Instances[index].RuntimePath != current.RuntimePath {
				return errors.New("instance runtime changed during publication update")
			}
			if path == "" && registry.Instances[index].Web != nil {
				return errors.New("clear the web profile before clearing the web publication")
			}
			registry.Instances[index].PublicationPath = path
			registry.Instances[index].PublicationID = id
			updated = registry.Instances[index]
			registry.Revision++
			return s.write(*registry)
		}
		return fmt.Errorf("instance %q is not registered", alias)
	})
	return updated, err
}

// UpdateWebProfile records the public posture of the web host. A nil profile
// restores the default local profile. The server profile needs a web publication.
func (s Store) UpdateWebProfile(alias string, profile *WebProfile) (Instance, error) {
	if !validAlias(alias) {
		return Instance{}, errors.New("invalid instance alias")
	}
	normalized, err := normalizeWebProfile(profile)
	if err != nil {
		return Instance{}, err
	}
	var updated Instance
	err = s.withLock(func(registry *Registry) error {
		for index := range registry.Instances {
			if registry.Instances[index].Alias != alias {
				continue
			}
			if normalized != nil && normalized.Mode == WebProfileServer && registry.Instances[index].PublicationPath == "" {
				return errors.New("server web profile requires a web publication")
			}
			for _, other := range registry.Instances {
				if other.Alias != alias && webAddressTaken(other, normalized) {
					return fmt.Errorf("web host address is already used by %q", other.Alias)
				}
			}
			registry.Instances[index].Web = normalized
			updated = registry.Instances[index]
			registry.Revision++
			return s.write(*registry)
		}
		return fmt.Errorf("instance %q is not registered", alias)
	})
	return updated, err
}

func webAddressTaken(other Instance, profile *WebProfile) bool {
	return profile != nil && profile.Address != "" && other.Web != nil && other.Web.Address == profile.Address
}

// normalizeWebProfile returns the canonical form to persist, or nil for the
// default local profile, so that equivalent configurations are stored once.
func normalizeWebProfile(profile *WebProfile) (*WebProfile, error) {
	if profile == nil {
		return nil, nil
	}
	copied := &WebProfile{Mode: profile.Mode, Address: strings.TrimSpace(profile.Address),
		AllowedHosts: append([]string(nil), profile.AllowedHosts...), PublicOrigins: append([]string(nil), profile.PublicOrigins...),
		TrustedProxies: append([]string(nil), profile.TrustedProxies...)}
	switch copied.Mode {
	case "", WebProfileLocal:
		if len(copied.AllowedHosts)+len(copied.PublicOrigins)+len(copied.TrustedProxies) != 0 {
			return nil, errors.New("local web profile cannot declare public hosts, origins or proxies")
		}
		if copied.Address == "" {
			return nil, nil
		}
		copied.Mode = WebProfileLocal
		if err := webprofile.ValidateLoopbackAddress(copied.Address); err != nil {
			return nil, err
		}
	case WebProfileServer:
		if copied.Address == "" {
			return nil, errors.New("server web profile requires a fixed loopback address")
		}
		if err := webprofile.ValidateServerProfile(copied.Address, copied.AllowedHosts, copied.PublicOrigins, copied.TrustedProxies); err != nil {
			return nil, fmt.Errorf("invalid server web profile: %w", err)
		}
	default:
		return nil, errors.New("web profile must be local or server")
	}
	sort.Strings(copied.AllowedHosts)
	sort.Strings(copied.PublicOrigins)
	sort.Strings(copied.TrustedProxies)
	return copied, nil
}

func (s Store) resolve(ctx context.Context, options AddOptions) (Instance, error) {
	if !validAlias(options.Alias) {
		return Instance{}, errors.New("alias must contain 1 to 64 lowercase letters, digits, '.', '_' or '-'")
	}
	root, rootObject, err := resolveDirectory(options.RootPath)
	if err != nil {
		return Instance{}, fmt.Errorf("invalid instance root: %w", err)
	}
	security := ""
	var securityObject *directoryobject.ID
	if options.SecurityDirectory != "" {
		var identity directoryobject.ID
		security, identity, err = resolveDirectory(options.SecurityDirectory)
		if err != nil {
			return Instance{}, fmt.Errorf("invalid security directory: %w", err)
		}
		securityObject = &identity
	}
	if (options.EnginePath == "") == (options.RuntimePath == "") {
		return Instance{}, errors.New("exactly one engine binary or runtime package is required")
	}
	var engine, runtimePath, runtimeID string
	var build buildinfo.Info
	if options.RuntimePath != "" {
		runtimePath, runtimeID, engine, build, err = s.resolveRuntimePackage(ctx, options.RuntimePath)
		if err != nil {
			return Instance{}, err
		}
	} else {
		engine, err = resolveFile(options.EnginePath)
		if err != nil {
			return Instance{}, fmt.Errorf("invalid engine binary: %w", err)
		}
		inspect := s.Inspect
		if inspect == nil {
			inspect = InspectRuntime
		}
		build, err = inspect(ctx, engine)
		if err != nil {
			return Instance{}, fmt.Errorf("engine build information unavailable: %w", err)
		}
		if !build.Valid() || build.OS != runtime.GOOS || build.Arch != runtime.GOARCH {
			return Instance{}, errors.New("engine build information is invalid or incompatible with this machine")
		}
	}
	mode := options.Supervision
	if mode == "" {
		mode = SupervisionManaged
	}
	if mode != SupervisionManaged && mode != SupervisionExternal {
		return Instance{}, errors.New("supervision must be managed or external")
	}
	if mode == SupervisionExternal && options.Autostart {
		return Instance{}, errors.New("autostart is incompatible with external supervision")
	}
	publicationPath, publicationID, err := s.resolvePublication(options.PublicationPath)
	if err != nil {
		return Instance{}, err
	}
	if publicationPath != "" && runtimePath == "" {
		return Instance{}, errors.New("web publication requires a packaged runtime")
	}
	if publicationPath != "" {
		if err := s.validateWebRuntime(ctx, runtimePath); err != nil {
			return Instance{}, err
		}
	}
	web, err := normalizeWebProfile(options.Web)
	if err != nil {
		return Instance{}, err
	}
	if web != nil && publicationPath == "" {
		return Instance{}, errors.New("web profile requires a web publication")
	}
	return Instance{Alias: options.Alias, RootPath: root, RootObject: rootObject,
		SecurityDirectory: security, SecurityObject: securityObject, EnginePath: engine, Build: build,
		RuntimePath: runtimePath, RuntimeID: runtimeID, PublicationPath: publicationPath, PublicationID: publicationID,
		Supervision: mode, Autostart: options.Autostart, Web: web}, nil
}

func (s Store) resolvePublication(value string) (string, string, error) {
	if strings.TrimSpace(value) == "" {
		return "", "", nil
	}
	inspect := s.InspectPublication
	if inspect == nil {
		inspect = publication.Inspect
	}
	inspection, err := inspect(value)
	if err != nil {
		return "", "", fmt.Errorf("web publication is invalid: %w", err)
	}
	if !filepath.IsAbs(inspection.Directory) || !publicationIDPattern.MatchString(inspection.PublicationID) {
		return "", "", errors.New("web publication inspection is invalid")
	}
	return inspection.Directory, inspection.PublicationID, nil
}

func (s Store) resolveRuntimePackage(ctx context.Context, value string) (string, string, string, buildinfo.Info, error) {
	inspect := s.InspectPackage
	if inspect == nil {
		inspect = InspectRuntimePackage
	}
	inspection, err := inspect(ctx, value)
	if err != nil {
		return "", "", "", buildinfo.Info{}, fmt.Errorf("runtime package is invalid: %w", err)
	}
	if !inspection.Compatible {
		return "", "", "", buildinfo.Info{}, errors.New("runtime package is incompatible with this machine")
	}
	engine, err := resolveFile(filepath.Join(inspection.Directory, filepath.FromSlash(inspection.Manifest.Entrypoints.Engine)))
	if err != nil {
		return "", "", "", buildinfo.Info{}, errors.New("runtime package engine is unavailable")
	}
	return inspection.Directory, inspection.Manifest.RuntimeID, engine, inspection.Manifest.EngineBuild, nil
}

func (s Store) validateWebRuntime(ctx context.Context, value string) error {
	inspect := s.InspectPackage
	if inspect == nil {
		inspect = InspectRuntimePackage
	}
	inspection, err := inspect(ctx, value)
	if err != nil || !inspection.Compatible {
		return errors.New("runtime package for web publication is unavailable or incompatible")
	}
	return validateWebRuntimeManifest(inspection.Manifest)
}

func validateWebRuntimeManifest(manifest RuntimeManifest) error {
	for _, required := range []string{"brique.spark-publication/1", "brique.spark-publication/2", "brique.spark-ui-catalog/1", "brique.spark-web-bootstrap/1"} {
		present := false
		for _, contract := range manifest.Contracts {
			present = present || contract == required
		}
		if !present {
			return fmt.Errorf("runtime does not declare %s", required)
		}
	}
	for _, component := range manifest.Components {
		if component.Name == "web-host" && component.Kind == "binary" {
			return nil
		}
	}
	return errors.New("runtime has no web-host component")
}

func InspectRuntime(ctx context.Context, engine string) (buildinfo.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, engine, "-build_info_json")
	var stdout limitedBuffer
	var stderr limitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return buildinfo.Info{}, errors.New("engine inspection timed out")
		}
		return buildinfo.Info{}, errors.New("engine inspection failed")
	}
	var info buildinfo.Info
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&info); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !info.Valid() {
		return buildinfo.Info{}, errors.New("invalid build-info response")
	}
	return info, nil
}

type limitedBuffer struct{ data []byte }

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	const limit = 64 << 10
	if len(buffer.data)+len(value) > limit {
		return 0, errors.New("process output exceeds limit")
	}
	buffer.data = append(buffer.data, value...)
	return len(value), nil
}
func (buffer *limitedBuffer) Bytes() []byte { return buffer.data }

func (s Store) withLock(operation func(*Registry) error) error {
	directory, err := filepath.Abs(s.Directory)
	if err != nil || s.Directory == "" {
		return errors.New("invalid launcher data directory")
	}
	registryDirectory := filepath.Join(directory, "registry")
	if err := protectedfile.CreateProtectedDirectory(registryDirectory); err != nil {
		return fmt.Errorf("cannot create launcher registry: %w", err)
	}
	if err := protectedfile.ValidateProtectedDirectory(directory, nil); err != nil {
		return fmt.Errorf("unsafe launcher data directory: %w", err)
	}
	if err := protectedfile.ValidateProtectedDirectory(registryDirectory, nil); err != nil {
		return fmt.Errorf("unsafe launcher registry directory: %w", err)
	}
	lock, err := protectedfile.OpenProtectedFile(filepath.Join(directory, filepath.FromSlash(registryLock)), true, nil, nil)
	if err != nil {
		return err
	}
	defer lock.Close()
	locked, err := protectedfile.LockFile(lock, true)
	if err != nil || !locked {
		return errors.New("launcher registry lock unavailable")
	}
	defer protectedfile.UnlockFile(lock)
	registry, migrated, err := s.read(directory)
	if err != nil {
		return err
	}
	if migrated {
		registry.Revision++
		if err := s.write(registry); err != nil {
			return fmt.Errorf("migrate launcher registry: %w", err)
		}
	}
	return operation(&registry)
}

func (s Store) read(directory string) (Registry, bool, error) {
	registry := Registry{Format: RegistryFormat, Instances: []Instance{}}
	path := filepath.Join(directory, filepath.FromSlash(registryRelative))
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return registry, false, nil
	} else if err != nil {
		return Registry{}, false, errors.New("launcher registry unavailable")
	}
	content, err := protectedfile.ReadProtectedFile(directory, registryRelative, protectedfile.ProtectedFileOptions{MaxBytes: maxRegistryBytes})
	if err != nil {
		return Registry{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Registry{}, false, errors.New("invalid launcher registry JSON")
	}
	migrated := registry.Format == legacyRegistryFormat || registry.Format == previousRegistryFormat || registry.Format == version3RegistryFormat
	if registry.Format != RegistryFormat {
		for _, instance := range registry.Instances {
			if instance.Web != nil {
				return Registry{}, false, errors.New("older launcher registry contains web profile fields")
			}
		}
	}
	if registry.Format == legacyRegistryFormat {
		for _, instance := range registry.Instances {
			if instance.RuntimePath != "" || instance.RuntimeID != "" || instance.PublicationPath != "" || instance.PublicationID != "" {
				return Registry{}, false, errors.New("legacy launcher registry contains runtime package fields")
			}
		}
	}
	if registry.Format == previousRegistryFormat {
		for _, instance := range registry.Instances {
			if instance.PublicationPath != "" || instance.PublicationID != "" {
				return Registry{}, false, errors.New("version 2 launcher registry contains publication fields")
			}
		}
	}
	if migrated {
		registry.Format = RegistryFormat
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, false, err
	}
	return registry, migrated, nil
}

func (s Store) write(registry Registry) error {
	content, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	directory, err := filepath.Abs(s.Directory)
	if err != nil {
		return errors.New("invalid launcher data directory")
	}
	return protectedfile.WriteProtectedFile(directory, registryRelative, content, 0o600)
}

func validateRegistry(registry Registry) error {
	if registry.Format != RegistryFormat || registry.Instances == nil {
		return errors.New("invalid launcher registry format")
	}
	aliases, roots, securityDirectories := map[string]bool{}, map[string]bool{}, map[string]bool{}
	webAddresses := map[string]bool{}
	for _, instance := range registry.Instances {
		securityInvalid := (instance.SecurityDirectory == "") != (instance.SecurityObject == nil)
		runtimeInvalid := (instance.RuntimePath == "") != (instance.RuntimeID == "")
		publicationInvalid := (instance.PublicationPath == "") != (instance.PublicationID == "")
		if instance.Web != nil {
			normalized, err := normalizeWebProfile(instance.Web)
			publicationInvalid = publicationInvalid || err != nil || normalized == nil || instance.PublicationPath == "" ||
				(instance.Web.Address != "" && webAddresses[instance.Web.Address])
			webAddresses[instance.Web.Address] = true
		}
		if instance.RuntimePath != "" {
			runtimeInvalid = runtimeInvalid || !filepath.IsAbs(instance.RuntimePath) || !runtimeIDPattern.MatchString(instance.RuntimeID)
		}
		if instance.SecurityObject != nil {
			securityInvalid = securityInvalid || !instance.SecurityObject.Valid() || securityDirectories[instance.SecurityObject.Key()]
		}
		if instance.PublicationPath != "" {
			publicationInvalid = publicationInvalid || !filepath.IsAbs(instance.PublicationPath) || !publicationIDPattern.MatchString(instance.PublicationID)
		}
		if !validAlias(instance.Alias) || aliases[instance.Alias] || !instance.RootObject.Valid() || roots[instance.RootObject.Key()] ||
			!filepath.IsAbs(instance.RootPath) || !filepath.IsAbs(instance.EnginePath) || !instance.Build.Valid() ||
			(instance.SecurityDirectory != "" && !filepath.IsAbs(instance.SecurityDirectory)) || securityInvalid || runtimeInvalid || publicationInvalid ||
			(instance.Supervision != SupervisionManaged && instance.Supervision != SupervisionExternal) ||
			(instance.Supervision == SupervisionExternal && instance.Autostart) {
			return errors.New("invalid launcher registry entry")
		}
		aliases[instance.Alias], roots[instance.RootObject.Key()] = true, true
		if instance.SecurityObject != nil {
			securityDirectories[instance.SecurityObject.Key()] = true
		}
	}
	return nil
}

func validAlias(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (index > 0 && (r == '.' || r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}

func resolveDirectory(value string) (string, directoryobject.ID, error) {
	if strings.TrimSpace(value) == "" {
		return "", directoryobject.ID{}, errors.New("directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", directoryobject.ID{}, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", directoryobject.ID{}, err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", directoryobject.ID{}, errors.New("not an existing directory")
	}
	identity, err := locking.DirectoryObjectIdentity(canonical)
	return canonical, identity, err
}

func resolveFile(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("file is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("not an existing regular file")
	}
	return canonical, nil
}

func cloneRegistry(value Registry) Registry {
	value.Instances = append([]Instance(nil), value.Instances...)
	return value
}
