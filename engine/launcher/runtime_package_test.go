/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"brique_engine/shared"
)

func testRuntimePackage(t *testing.T) (string, RuntimeManifest, shared.BuildInfo) {
	t.Helper()
	directory := t.TempDir()
	build := shared.BuildInfo{Format: shared.BuildInfoFormat, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	files := map[string][]byte{
		"bin/brique":          []byte("engine"),
		"bin/brique-sec":      []byte("security"),
		"bin/brique-launcher": []byte("launcher"),
		"bin/brique-web":      []byte("web-host"),
		"lib/spark/index.js":  []byte("spark"),
		"lib/pulse/index.js":  []byte("pulse"),
	}
	inventory := make([]RuntimeFile, 0, len(files))
	for name, content := range files {
		full := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if len(name) >= 4 && name[:4] == "bin/" {
			mode = 0o700
		}
		if err := os.WriteFile(full, content, mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		inventory = append(inventory, RuntimeFile{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Path < inventory[j].Path })
	manifest := RuntimeManifest{
		Format: RuntimeManifestFormat, RuntimeID: "brt1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		CreatedAt: "2026-11-14T12:00:00Z", Platform: RuntimePlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Provenance: RuntimeProvenance{VCSRevision: "abc", SourceTime: "2026-11-14T12:00:00Z"},
		Toolchains: RuntimeToolchains{Go: runtime.Version(), Node: "v24", NPM: "11", TypeScript: "6"}, EngineBuild: build,
		Contracts:   []string{"brique.build-info/1", "brique.runtime/1"},
		Entrypoints: RuntimeEntrypoints{Engine: "bin/brique", Security: "bin/brique-sec", Launcher: "bin/brique-launcher"},
		Components: []RuntimeComponent{
			{Name: "engine", Kind: "binary", Path: "bin/brique"},
			{Name: "security", Kind: "binary", Path: "bin/brique-sec"},
			{Name: "launcher", Kind: "binary", Path: "bin/brique-launcher"},
			{Name: "web-host", Kind: "binary", Path: "bin/brique-web"},
			{Name: "spark", Kind: "assets", Path: "lib/spark"},
			{Name: "pulse", Kind: "assets", Path: "lib/pulse"},
		},
		Files: inventory,
	}
	manifest.RuntimeID = runtimeManifestID(manifest)
	writeRuntimeManifest(t, directory, manifest)
	return directory, manifest, build
}

func TestInspectRuntimePackageRejectsNonExecutableBinaryComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows executability is not represented by Unix mode bits")
	}
	directory, manifest, build := testRuntimePackage(t)
	webHost := filepath.Join(directory, "bin", "brique-web")
	if err := os.Chmod(webHost, 0o600); err != nil {
		t.Fatal(err)
	}
	// Mode is not part of the content inventory or runtime ID, so component
	// verification must reject it independently.
	manifest.RuntimeID = runtimeManifestID(manifest)
	writeRuntimeManifest(t, directory, manifest)
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("non-executable runtime component accepted")
	}
}

func writeRuntimeManifest(t *testing.T, directory string, manifest RuntimeManifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, RuntimeManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInspectRuntimePackageVerifiesInventoryAndEngine(t *testing.T) {
	directory, manifest, build := testRuntimePackage(t)
	inspection, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Compatible || inspection.Manifest.RuntimeID != manifest.RuntimeID || len(inspection.Problems) != 0 {
		t.Fatalf("unexpected inspection: %#v", inspection)
	}
}

func TestInspectRuntimePackageRejectsTamperingAndExtraFiles(t *testing.T) {
	directory, _, build := testRuntimePackage(t)
	if err := os.WriteFile(filepath.Join(directory, "lib", "spark", "index.js"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("tampered runtime accepted")
	}

	directory, _, build = testRuntimePackage(t)
	if err := os.WriteFile(filepath.Join(directory, "extra"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("unlisted runtime file accepted")
	}
}

func TestInspectRuntimePackageRejectsARewrittenRuntimeID(t *testing.T) {
	directory, manifest, build := testRuntimePackage(t)
	manifest.RuntimeID = "brt1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	writeRuntimeManifest(t, directory, manifest)
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("rewritten runtime identifier accepted")
	}
}

func TestInspectRuntimePackageDiagnosesForeignPlatformWithoutExecuting(t *testing.T) {
	directory, manifest, _ := testRuntimePackage(t)
	manifest.Platform.OS = "foreign-os"
	manifest.RuntimeID = runtimeManifestID(manifest)
	writeRuntimeManifest(t, directory, manifest)
	called := false
	inspection, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) {
		called = true
		return shared.BuildInfo{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Compatible || called || len(inspection.Problems) != 1 {
		t.Fatalf("unexpected foreign inspection: %#v, called=%v", inspection, called)
	}
}

func TestInspectRuntimePackageRejectsUnknownManifestFieldsAndSymlinks(t *testing.T) {
	directory, _, build := testRuntimePackage(t)
	manifestPath := filepath.Join(directory, RuntimeManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] = ','
	raw = append(raw, []byte(`"unknown":true}`)...)
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("unknown manifest field accepted")
	}

	if runtime.GOOS != "windows" {
		directory, _, build = testRuntimePackage(t)
		if err := os.Symlink(filepath.Join(directory, "lib", "pulse", "index.js"), filepath.Join(directory, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
			t.Fatal("runtime symlink accepted")
		}
	}
}

// T-28: a runtime made of components that do not belong together is refused, and
// nothing is updated silently.
func TestInspectRuntimePackageRejectsAReplacedBriqueSecurityBinary(t *testing.T) {
	directory, _, build := testRuntimePackage(t)
	if err := os.WriteFile(filepath.Join(directory, "bin", "brique-sec"), []byte("another security binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil })
	if err == nil {
		t.Fatal("a runtime whose brique-sec differs from its inventory was accepted")
	}
}

func TestInspectRuntimePackageRejectsAPartialArtifact(t *testing.T) {
	directory, _, build := testRuntimePackage(t)
	if err := os.Remove(filepath.Join(directory, "bin", "brique-launcher")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntimePackage(context.Background(), directory, func(context.Context, string) (shared.BuildInfo, error) { return build, nil }); err == nil {
		t.Fatal("a runtime missing an inventoried binary was accepted")
	}
}

func TestWebRuntimeRequiresTheProtocolsAndTheWebHostItServes(t *testing.T) {
	complete := RuntimeManifest{
		Contracts:  []string{"brique.spark-publication/1", "brique.spark-publication/2", "brique.spark-ui-catalog/1", "brique.spark-web-bootstrap/1"},
		Components: []RuntimeComponent{{Name: "web-host", Kind: "binary", Path: "bin/brique-web"}},
	}
	if err := validateWebRuntimeManifest(complete); err != nil {
		t.Fatalf("a complete web runtime was refused: %v", err)
	}
	for _, missing := range complete.Contracts {
		partial := complete
		partial.Contracts = nil
		for _, contract := range complete.Contracts {
			if contract != missing {
				partial.Contracts = append(partial.Contracts, contract)
			}
		}
		if err := validateWebRuntimeManifest(partial); err == nil {
			t.Fatalf("a runtime without %s was accepted for web publication", missing)
		}
	}
	noHost := complete
	noHost.Components = nil
	if err := validateWebRuntimeManifest(noHost); err == nil {
		t.Fatal("a runtime without a web host was accepted")
	}
}
