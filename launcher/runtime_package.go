/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/platform/protectedfile"
)

const (
	RuntimeManifestFormat = "brique.runtime/1"
	RuntimeManifestName   = "brique-runtime.json"
	maxRuntimeManifest    = 1 << 20
)

var runtimeIDPattern = regexp.MustCompile(`^brt1_[A-Za-z0-9_-]{43}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type RuntimePlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type RuntimeProvenance struct {
	VCSRevision string `json:"vcs_revision,omitempty"`
	VCSModified bool   `json:"vcs_modified"`
	SourceTime  string `json:"source_time"`
}

type RuntimeToolchains struct {
	Go         string `json:"go"`
	Node       string `json:"node"`
	NPM        string `json:"npm"`
	TypeScript string `json:"typescript"`
}

type RuntimeEntrypoints struct {
	Engine   string `json:"engine"`
	Security string `json:"security"`
	Launcher string `json:"launcher"`
}

type RuntimeComponent struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type RuntimeFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type RuntimeManifest struct {
	Format      string             `json:"format"`
	RuntimeID   string             `json:"runtime_id"`
	CreatedAt   string             `json:"created_at"`
	Platform    RuntimePlatform    `json:"platform"`
	Provenance  RuntimeProvenance  `json:"provenance"`
	Toolchains  RuntimeToolchains  `json:"toolchains"`
	EngineBuild buildinfo.Info     `json:"engine_build"`
	Contracts   []string           `json:"contracts"`
	Entrypoints RuntimeEntrypoints `json:"entrypoints"`
	Components  []RuntimeComponent `json:"components"`
	Files       []RuntimeFile      `json:"files"`
}

type RuntimeInspection struct {
	Directory  string          `json:"directory"`
	Manifest   RuntimeManifest `json:"manifest"`
	Compatible bool            `json:"compatible"`
	Problems   []string        `json:"problems"`
}

func InspectRuntimePackage(ctx context.Context, directory string) (RuntimeInspection, error) {
	return inspectRuntimePackage(ctx, directory, InspectRuntime)
}

func inspectRuntimePackage(ctx context.Context, directory string, inspect RuntimeInspector) (RuntimeInspection, error) {
	canonical, err := canonicalRuntimeDirectory(directory)
	if err != nil {
		return RuntimeInspection{}, err
	}
	manifest, err := loadRuntimeManifest(filepath.Join(canonical, RuntimeManifestName))
	if err != nil {
		return RuntimeInspection{}, err
	}
	if err := validateRuntimeManifest(manifest); err != nil {
		return RuntimeInspection{}, err
	}
	if err := verifyRuntimeFiles(canonical, manifest.Files); err != nil {
		return RuntimeInspection{}, err
	}
	if err := verifyRuntimeComponents(canonical, manifest); err != nil {
		return RuntimeInspection{}, err
	}

	result := RuntimeInspection{Directory: canonical, Manifest: manifest, Compatible: true, Problems: []string{}}
	if manifest.Platform.OS != runtime.GOOS || manifest.Platform.Arch != runtime.GOARCH {
		result.Compatible = false
		result.Problems = append(result.Problems, "runtime platform does not match this machine")
		return result, nil
	}
	build, err := inspect(ctx, filepath.Join(canonical, filepath.FromSlash(manifest.Entrypoints.Engine)))
	if err != nil {
		return RuntimeInspection{}, errors.New("runtime engine inspection failed")
	}
	if !reflect.DeepEqual(build, manifest.EngineBuild) {
		return RuntimeInspection{}, errors.New("runtime engine build information does not match manifest")
	}
	return result, nil
}

func canonicalRuntimeDirectory(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", errors.New("runtime directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", errors.New("invalid runtime directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("runtime directory is unavailable")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("runtime path is not a directory")
	}
	return canonical, nil
}

func loadRuntimeManifest(manifestPath string) (RuntimeManifest, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return RuntimeManifest{}, errors.New("runtime manifest is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxRuntimeManifest+1))
	if err != nil || len(raw) == 0 || len(raw) > maxRuntimeManifest {
		return RuntimeManifest{}, errors.New("invalid runtime manifest size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest RuntimeManifest
	if err := decoder.Decode(&manifest); err != nil {
		return RuntimeManifest{}, errors.New("invalid runtime manifest JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RuntimeManifest{}, errors.New("invalid trailing runtime manifest data")
	}
	return manifest, nil
}

func validateRuntimeManifest(manifest RuntimeManifest) error {
	created, err := time.Parse(time.RFC3339, manifest.CreatedAt)
	if err != nil || created.Location() != time.UTC || manifest.Format != RuntimeManifestFormat ||
		!runtimeIDPattern.MatchString(manifest.RuntimeID) || manifest.Platform.OS == "" || manifest.Platform.Arch == "" ||
		manifest.Provenance.SourceTime == "" || manifest.Toolchains.Go == "" || manifest.Toolchains.Node == "" ||
		manifest.Toolchains.NPM == "" || manifest.Toolchains.TypeScript == "" || !manifest.EngineBuild.Valid() {
		return errors.New("invalid runtime manifest identity")
	}
	if !strictSortedUnique(manifest.Contracts) || len(manifest.Contracts) == 0 || len(manifest.Components) == 0 || len(manifest.Files) == 0 {
		return errors.New("invalid runtime manifest inventory")
	}
	entrypoints := []string{manifest.Entrypoints.Engine, manifest.Entrypoints.Security, manifest.Entrypoints.Launcher}
	for _, value := range entrypoints {
		if !protectedfile.ValidProtectedRelativePath(value) {
			return errors.New("invalid runtime entrypoint path")
		}
	}
	previous := ""
	for _, file := range manifest.Files {
		if !protectedfile.ValidProtectedRelativePath(file.Path) || file.Path == RuntimeManifestName || file.Path <= previous || file.Size < 0 || !sha256Pattern.MatchString(file.SHA256) {
			return errors.New("invalid runtime file inventory")
		}
		previous = file.Path
	}
	names := map[string]bool{}
	for _, component := range manifest.Components {
		if component.Name == "" || names[component.Name] || (component.Kind != "binary" && component.Kind != "assets") || !protectedfile.ValidProtectedRelativePath(component.Path) {
			return errors.New("invalid runtime component inventory")
		}
		names[component.Name] = true
	}
	for _, required := range []string{"engine", "security", "launcher", "spark", "pulse"} {
		if !names[required] {
			return errors.New("runtime manifest is missing a required component")
		}
	}
	if manifest.RuntimeID != runtimeManifestID(manifest) {
		return errors.New("runtime identifier does not match manifest content")
	}
	return nil
}

func runtimeManifestID(manifest RuntimeManifest) string {
	var payload strings.Builder
	add := func(value string) {
		payload.WriteString(strconv.Itoa(len(value)))
		payload.WriteByte(':')
		payload.WriteString(value)
		payload.WriteByte('\n')
	}
	add(manifest.Format)
	add(manifest.Platform.OS)
	add(manifest.Platform.Arch)
	add(manifest.Provenance.VCSRevision)
	add(strconv.FormatBool(manifest.Provenance.VCSModified))
	add(manifest.Provenance.SourceTime)
	add(manifest.Toolchains.Go)
	add(manifest.Toolchains.Node)
	add(manifest.Toolchains.NPM)
	add(manifest.Toolchains.TypeScript)
	add(manifest.EngineBuild.Format)
	add(manifest.EngineBuild.EngineVersion)
	add(manifest.EngineBuild.VCSRevision)
	if manifest.EngineBuild.VCSModified == nil {
		add("unset")
	} else {
		add(strconv.FormatBool(*manifest.EngineBuild.VCSModified))
	}
	add(manifest.EngineBuild.VCSTime)
	add(manifest.EngineBuild.GoVersion)
	add(manifest.EngineBuild.OS)
	add(manifest.EngineBuild.Arch)
	add(manifest.Entrypoints.Engine)
	add(manifest.Entrypoints.Security)
	add(manifest.Entrypoints.Launcher)
	add(strconv.Itoa(len(manifest.Components)))
	for _, component := range manifest.Components {
		add(component.Name)
		add(component.Kind)
		add(component.Path)
	}
	add(strconv.Itoa(len(manifest.Contracts)))
	for _, contract := range manifest.Contracts {
		add(contract)
	}
	add(strconv.Itoa(len(manifest.Files)))
	for _, file := range manifest.Files {
		add(file.Path)
		add(strconv.FormatInt(file.Size, 10))
		add(file.SHA256)
	}
	digest := sha256.Sum256([]byte(payload.String()))
	return "brt1_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func verifyRuntimeFiles(directory string, expected []RuntimeFile) error {
	actual := make([]RuntimeFile, 0, len(expected))
	err := filepath.WalkDir(directory, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("runtime package contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		if relative == RuntimeManifestName {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("runtime package contains a non-regular file")
		}
		file, err := os.Open(current)
		if err != nil {
			return errors.New("runtime package file is unreadable")
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("runtime package file is unreadable")
		}
		actual = append(actual, RuntimeFile{Path: relative, Size: info.Size(), SHA256: hex.EncodeToString(digest.Sum(nil))})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Path < actual[j].Path })
	if len(actual) != len(expected) {
		return errors.New("runtime package file inventory does not match manifest")
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return errors.New("runtime package file integrity check failed")
		}
	}
	return nil
}

func verifyRuntimeComponents(directory string, manifest RuntimeManifest) error {
	files := map[string]bool{}
	for _, file := range manifest.Files {
		files[file.Path] = true
	}
	for _, entrypoint := range []string{manifest.Entrypoints.Engine, manifest.Entrypoints.Security, manifest.Entrypoints.Launcher} {
		if !files[entrypoint] {
			return errors.New("runtime entrypoint is absent from file inventory")
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(filepath.Join(directory, filepath.FromSlash(entrypoint)))
			if err != nil || info.Mode().Perm()&0o111 == 0 {
				return errors.New("runtime binary entrypoint is not executable")
			}
		}
	}
	for _, component := range manifest.Components {
		info, err := os.Stat(filepath.Join(directory, filepath.FromSlash(component.Path)))
		if err != nil || (component.Kind == "binary" && !info.Mode().IsRegular()) || (component.Kind == "assets" && !info.IsDir()) {
			return errors.New("runtime component path is unavailable")
		}
		if component.Kind == "binary" && runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return errors.New("runtime binary component is not executable")
		}
	}
	return nil
}

func strictSortedUnique(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		if value == "" || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}
