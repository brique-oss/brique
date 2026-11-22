/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"brique_engine/contracts/buildinfo"
	"brique_engine/contracts/publication"
)

func testInspector(context.Context, string) (buildinfo.Info, error) {
	return buildinfo.Info{Format: buildinfo.Format, EngineVersion: "test", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}

func testPublicationInspection(t *testing.T, publicationID string) (string, PublicationInspector) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "publication")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	inspection := publication.Inspection{Directory: canonical, PublicationID: publicationID}
	return canonical, func(string) (publication.Inspection, error) { return inspection, nil }
}

func testStore(t *testing.T) (Store, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "instance", "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(base, "engine")
	if err := os.WriteFile(engine, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Store{Directory: filepath.Join(base, "launcher"), Inspect: testInspector}, root, engine
}

func testRuntimeInspection(t *testing.T, build buildinfo.Info, runtimeID string) (string, RuntimePackageInspector) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "runtime")
	engine := filepath.Join(directory, "bin", "brique")
	if err := os.MkdirAll(filepath.Dir(engine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine, []byte("engine"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	inspection := RuntimeInspection{Directory: canonical, Compatible: true, Problems: []string{}, Manifest: RuntimeManifest{
		Format: RuntimeManifestFormat, RuntimeID: runtimeID, Platform: RuntimePlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		EngineBuild: build, Entrypoints: RuntimeEntrypoints{Engine: "bin/brique"},
	}}
	return canonical, func(context.Context, string) (RuntimeInspection, error) { return inspection, nil }
}

func withWebRuntimeContracts(base RuntimePackageInspector) RuntimePackageInspector {
	return func(ctx context.Context, path string) (RuntimeInspection, error) {
		inspection, err := base(ctx, path)
		inspection.Manifest.Contracts = []string{"brique.spark-publication/1", "brique.spark-publication/2", "brique.spark-ui-catalog/1", "brique.spark-web-bootstrap/1"}
		inspection.Manifest.Components = []RuntimeComponent{{Name: "web-host", Kind: "binary", Path: "bin/brique-web"}}
		return inspection, err
	}
}

func TestRegistryPersistsCanonicalInstance(t *testing.T) {
	store, root, engine := testStore(t)
	instance, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine})
	if err != nil {
		t.Fatal(err)
	}
	if instance.Supervision != SupervisionManaged || !instance.RootObject.Valid() {
		t.Fatalf("unexpected instance: %#v", instance)
	}
	registry, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Format != RegistryFormat || registry.Revision != 1 || len(registry.Instances) != 1 || registry.Instances[0].Alias != "alpha" {
		t.Fatalf("unexpected registry: %#v", registry)
	}
}

func TestRegistryRejectsAliasAndRootDuplicates(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(root), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: other, EnginePath: engine}); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "beta", RootPath: root, EnginePath: engine}); err == nil {
		t.Fatal("duplicate root accepted")
	}
}

func TestRegistryRejectsDuplicateSecurityDirectoryObject(t *testing.T) {
	store, root, engine := testStore(t)
	security := filepath.Join(filepath.Dir(root), "security-state")
	if err := os.Mkdir(security, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine, SecurityDirectory: security}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(root), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	securityAlias := filepath.Join(filepath.Dir(root), "security-alias")
	if err := os.Symlink(security, securityAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "beta", RootPath: other, EnginePath: engine, SecurityDirectory: securityAlias}); err == nil {
		t.Fatal("duplicate security directory object accepted through a symlink")
	}
}

func TestRemoveNeverDeletesInstanceFiles(t *testing.T) {
	store, root, engine := testStore(t)
	marker := filepath.Join(root, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("instance file changed: %q, %v", content, err)
	}
	registry, err := store.List()
	if err != nil || registry.Revision != 2 || len(registry.Instances) != 0 {
		t.Fatalf("unexpected registry after removal: %#v, %v", registry, err)
	}
}

func TestRegistrySerializesConcurrentAdds(t *testing.T) {
	store, _, engine := testStore(t)
	const count = 8
	var wait sync.WaitGroup
	errorsFound := make(chan error, count)
	for index := 0; index < count; index++ {
		root := filepath.Join(filepath.Dir(engine), "roots", string(rune('a'+index)))
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		wait.Add(1)
		go func(alias, root string) {
			defer wait.Done()
			_, err := store.Add(context.Background(), AddOptions{Alias: alias, RootPath: root, EnginePath: engine})
			errorsFound <- err
		}("instance-"+string(rune('a'+index)), root)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	registry, err := store.List()
	if err != nil || len(registry.Instances) != count || registry.Revision != count {
		t.Fatalf("lost concurrent update: %#v, %v", registry, err)
	}
}

func TestExternalSupervisionRejectsAutostart(t *testing.T) {
	store, root, engine := testStore(t)
	_, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine, Supervision: SupervisionExternal, Autostart: true})
	if err == nil {
		t.Fatal("external autostart accepted")
	}
}

func TestRegistryUpdatesOnlyTheValidatedEngine(t *testing.T) {
	store, root, engine := testStore(t)
	original, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine})
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(engine), "replacement-engine")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateEngine(context.Background(), "alpha", replacement)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(replacement)
	if updated.EnginePath != canonical || updated.RootPath != original.RootPath {
		t.Fatalf("unexpected update: %#v", updated)
	}
	registry, err := store.List()
	if err != nil || registry.Revision != 2 || registry.Instances[0].EnginePath != canonical {
		t.Fatalf("update was not persisted: %#v, %v", registry, err)
	}
}

func TestRegistryAssociatesAndUpdatesRuntimePackage(t *testing.T) {
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	firstID := "brt1_" + strings.Repeat("a", 43)
	firstRuntime, inspectFirst := testRuntimeInspection(t, build, firstID)
	store.InspectPackage = inspectFirst
	instance, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: firstRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if instance.RuntimePath != firstRuntime || instance.RuntimeID != firstID || instance.EnginePath != filepath.Join(firstRuntime, "bin", "brique") {
		t.Fatalf("unexpected runtime association: %#v", instance)
	}
	secondID := "brt1_" + strings.Repeat("b", 43)
	secondRuntime, inspectSecond := testRuntimeInspection(t, build, secondID)
	store.InspectPackage = inspectSecond
	updated, err := store.UpdateRuntime(context.Background(), "alpha", secondRuntime)
	if err != nil || updated.RuntimeID != secondID || updated.RuntimePath != secondRuntime {
		t.Fatalf("runtime update = %#v, %v", updated, err)
	}
	standalone, err := store.UpdateEngine(context.Background(), "alpha", engine)
	if err != nil || standalone.RuntimePath != "" || standalone.RuntimeID != "" {
		t.Fatalf("standalone engine did not clear runtime association: %#v, %v", standalone, err)
	}
}

func TestRegistryAssociatesUpdatesAndClearsWebPublication(t *testing.T) {
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	runtimePath, inspectRuntime := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("c", 43))
	store.InspectPackage = withWebRuntimeContracts(inspectRuntime)
	firstID := "spub1_" + strings.Repeat("a", 43)
	firstPath, inspectFirst := testPublicationInspection(t, firstID)
	store.InspectPublication = inspectFirst
	instance, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath, PublicationPath: firstPath})
	if err != nil || instance.PublicationPath != firstPath || instance.PublicationID != firstID {
		t.Fatalf("publication association = %#v, %v", instance, err)
	}
	secondID := "spub1_" + strings.Repeat("b", 43)
	secondPath, inspectSecond := testPublicationInspection(t, secondID)
	store.InspectPublication = inspectSecond
	updated, err := store.UpdatePublication(context.Background(), "alpha", secondPath)
	if err != nil || updated.PublicationPath != secondPath || updated.PublicationID != secondID {
		t.Fatalf("publication update = %#v, %v", updated, err)
	}
	cleared, err := store.UpdatePublication(context.Background(), "alpha", "")
	if err != nil || cleared.PublicationPath != "" || cleared.PublicationID != "" {
		t.Fatalf("publication clear = %#v, %v", cleared, err)
	}
}

func TestRegistryRequiresExactlyOneRuntimeSource(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "missing", RootPath: root}); err == nil {
		t.Fatal("instance without engine or runtime accepted")
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "both", RootPath: root, EnginePath: engine, RuntimePath: filepath.Dir(engine)}); err == nil {
		t.Fatal("instance with engine and runtime accepted")
	}
}

func TestRegistryMigratesLegacyFormatAtomically(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, filepath.FromSlash(registryRelative))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(raw), RegistryFormat, legacyRegistryFormat, 1)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Format != RegistryFormat || registry.Revision != 2 || len(registry.Instances) != 1 {
		t.Fatalf("unexpected migrated registry: %#v", registry)
	}
	persisted, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(persisted), `"format": "`+RegistryFormat+`"`) {
		t.Fatalf("migration was not persisted: %s, %v", persisted, err)
	}
}

func TestRegistryMigratesVersion2FormatAtomically(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, filepath.FromSlash(registryRelative))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	version2 := strings.Replace(string(raw), RegistryFormat, previousRegistryFormat, 1)
	if err := os.WriteFile(path, []byte(version2), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.List()
	if err != nil || registry.Format != RegistryFormat || registry.Revision != 2 {
		t.Fatalf("version 2 migration = %#v, %v", registry, err)
	}
}

func TestRegistryRejectsRuntimeFieldsInLegacyFormat(t *testing.T) {
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	runtimePath, inspect := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("c", 43))
	store.InspectPackage = inspect
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, filepath.FromSlash(registryRelative))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(raw), RegistryFormat, legacyRegistryFormat, 1)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("legacy registry with runtime fields accepted")
	}
}

func TestRegistryRejectsMalformedTrailingJSON(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, filepath.FromSlash(registryRelative))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '{'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("malformed trailing registry data accepted")
	}
}

func serverProfile() *WebProfile {
	return &WebProfile{Mode: WebProfileServer, Address: "127.0.0.1:18080", AllowedHosts: []string{"Brique.Example.org"},
		PublicOrigins: []string{"https://Brique.Example.org"}, TrustedProxies: []string{"10.0.0.2"}}
}

func webStore(t *testing.T) (Store, string, string, string) {
	t.Helper()
	store, root, engine := testStore(t)
	build, _ := store.Inspect(context.Background(), engine)
	runtimePath, inspectRuntime := testRuntimeInspection(t, build, "brt1_"+strings.Repeat("c", 43))
	store.InspectPackage = withWebRuntimeContracts(inspectRuntime)
	publicationPath, inspectPublication := testPublicationInspection(t, "spub1_"+strings.Repeat("a", 43))
	store.InspectPublication = inspectPublication
	return store, root, runtimePath, publicationPath
}

func TestRegistryPersistsAndNormalizesServerWebProfile(t *testing.T) {
	store, root, runtimePath, publicationPath := webStore(t)
	profile := &WebProfile{Mode: WebProfileServer, Address: "127.0.0.1:18080", AllowedHosts: []string{"brique.example.org"},
		PublicOrigins: []string{"https://brique.example.org"}, TrustedProxies: []string{"10.0.0.2"}}
	instance, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath, PublicationPath: publicationPath, Web: profile})
	if err != nil || instance.Web == nil || instance.Web.Address != "127.0.0.1:18080" {
		t.Fatalf("server profile = %#v, %v", instance.Web, err)
	}
	reloaded, err := store.Get("alpha")
	if err != nil || reloaded.Web == nil || reloaded.Web.Mode != WebProfileServer {
		t.Fatalf("server profile not persisted: %#v, %v", reloaded.Web, err)
	}
	arguments := strings.Join(webHostArguments(reloaded), " ")
	for _, expected := range []string{"-addr 127.0.0.1:18080", "-allowed_host brique.example.org", "-public_origin https://brique.example.org", "-trusted_proxy 10.0.0.2"} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("web host arguments %q lack %q", arguments, expected)
		}
	}
}

func TestRegistryRefusesIncoherentWebProfiles(t *testing.T) {
	store, root, runtimePath, publicationPath := webStore(t)
	mutate := func(change func(*WebProfile)) *WebProfile {
		profile := serverProfile()
		change(profile)
		return profile
	}
	cases := map[string]*WebProfile{
		"remote http origin":      mutate(func(p *WebProfile) { p.PublicOrigins = []string{"http://brique.example.org"} }),
		"non loopback listen":     mutate(func(p *WebProfile) { p.Address = "0.0.0.0:18080" }),
		"dynamic port":            mutate(func(p *WebProfile) { p.Address = "127.0.0.1:0" }),
		"missing host":            mutate(func(p *WebProfile) { p.AllowedHosts = nil }),
		"missing origin":          mutate(func(p *WebProfile) { p.PublicOrigins = nil }),
		"missing proxy":           mutate(func(p *WebProfile) { p.TrustedProxies = nil }),
		"invalid proxy":           mutate(func(p *WebProfile) { p.TrustedProxies = []string{"proxy.example.org"} }),
		"origin without host":     mutate(func(p *WebProfile) { p.PublicOrigins = []string{"https://other.example.org"} }),
		"host with path":          mutate(func(p *WebProfile) { p.AllowedHosts = []string{"brique.example.org/x"} }),
		"unknown mode":            {Mode: "public", Address: "127.0.0.1:18080"},
		"local with public hosts": {Mode: WebProfileLocal, AllowedHosts: []string{"brique.example.org"}},
	}
	for name, profile := range cases {
		if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath, PublicationPath: publicationPath, Web: profile}); err == nil {
			t.Fatalf("%s: incoherent profile accepted", name)
		}
	}
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath, Web: serverProfile()}); err == nil {
		t.Fatal("web profile without a publication accepted")
	}
}

func TestRegistryUpdatesWebProfileAndRefusesAddressCollisionAndPublicationClear(t *testing.T) {
	store, root, runtimePath, publicationPath := webStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, RuntimePath: runtimePath, PublicationPath: publicationPath}); err != nil {
		t.Fatal(err)
	}
	if updated, err := store.UpdateWebProfile("alpha", serverProfile()); err != nil || updated.Web == nil {
		t.Fatalf("update web profile = %#v, %v", updated, err)
	}
	if _, err := store.UpdatePublication(context.Background(), "alpha", ""); err == nil {
		t.Fatal("publication cleared under a web profile")
	}
	otherRoot := t.TempDir()
	if _, err := store.Add(context.Background(), AddOptions{Alias: "beta", RootPath: otherRoot, RuntimePath: runtimePath, PublicationPath: publicationPath, Web: serverProfile()}); err == nil {
		t.Fatal("two instances accepted the same fixed web address")
	}
	cleared, err := store.UpdateWebProfile("alpha", nil)
	if err != nil || cleared.Web != nil {
		t.Fatalf("clear web profile = %#v, %v", cleared, err)
	}
	if _, err := store.UpdatePublication(context.Background(), "alpha", ""); err != nil {
		t.Fatalf("publication clear after profile clear: %v", err)
	}
	if _, err := store.UpdateWebProfile("alpha", serverProfile()); err == nil {
		t.Fatal("server profile accepted without publication")
	}
}

func TestRegistryMigratesVersion3AndRefusesWebProfileInOlderFormats(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory, filepath.FromSlash(registryRelative))
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), RegistryFormat, version3RegistryFormat, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.List()
	if err != nil || registry.Format != RegistryFormat {
		t.Fatalf("version 3 migration = %#v, %v", registry, err)
	}
	raw, _ = os.ReadFile(path)
	forged := strings.Replace(string(raw), RegistryFormat, version3RegistryFormat, 1)
	forged = strings.Replace(forged, `"supervision"`, `"web": {"mode": "local", "address": "127.0.0.1:1"}, "supervision"`, 1)
	if err := os.WriteFile(path, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("version 3 registry carrying a web profile accepted")
	}
}

func TestSetAutostartChangesOnlyTheFlag(t *testing.T) {
	store, root, engine := testStore(t)
	if _, err := store.Add(context.Background(), AddOptions{Alias: "demo", RootPath: root, EnginePath: engine}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.List()
	on, err := store.SetAutostart("demo", true)
	if err != nil || !on.Autostart {
		t.Fatalf("on: %+v %v", on, err)
	}
	after, _ := store.List()
	if after.Revision != before.Revision+1 || after.Instances[0].EnginePath != before.Instances[0].EnginePath {
		t.Fatalf("revision/engine: %d→%d", before.Revision, after.Revision)
	}
	if again, err := store.SetAutostart("demo", true); err != nil || !again.Autostart {
		t.Fatalf("idempotent: %+v %v", again, err)
	}
	if unchanged, _ := store.List(); unchanged.Revision != after.Revision {
		t.Fatal("an unchanged flag bumped the revision")
	}
	if off, err := store.SetAutostart("demo", false); err != nil || off.Autostart {
		t.Fatalf("off: %+v %v", off, err)
	}
	if _, err := store.SetAutostart("missing", true); err == nil {
		t.Fatal("unknown instance accepted")
	}
	if _, err := store.SetAutostart("../x", true); err == nil {
		t.Fatal("bad alias accepted")
	}
}
