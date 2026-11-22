/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeContext(t *testing.T, path, uiConfig string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ui := ""
	if uiConfig != "" {
		ui = `,"ui_config":` + uiConfig
	}
	if err := os.WriteFile(path, []byte(`{"brique":{"ctx_name":"x"`+ui+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWebRootIsTheRootUIOrAChildNamedWeb(t *testing.T) {
	web := `{"adapter":"react","root_view":"Home"}`
	editor := `{"adapter":"react","root_view":"BriqueEditorRoot"}`

	root := t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), web)
	if got, err := webRootFor(root); err != nil || got != "" {
		t.Fatalf("a web root UI needs no argument: %q %v", got, err)
	}

	root = t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), editor)
	writeContext(t, filepath.Join(root, "web", "context.json"), web)
	if got, err := webRootFor(root); err != nil || got != "/root/web" {
		t.Fatalf("an editor root names the web child: %q %v", got, err)
	}

	root = t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), editor)
	if _, err := webRootFor(root); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("an editor root without a web child must say what is missing: %v", err)
	}

	root = t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), editor)
	writeContext(t, filepath.Join(root, "web", "context.json"), editor)
	if _, err := webRootFor(root); err == nil {
		t.Fatal("an editor UI is not a web home page")
	}

	root = t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), "")
	writeContext(t, filepath.Join(root, "web", "context.json"), web)
	if got, err := webRootFor(root); err != nil || got != "/root/web" {
		t.Fatalf("a root without UI uses the web child: %q %v", got, err)
	}
}

func TestFingerprintFollowsTheInterfacesAndNothingElse(t *testing.T) {
	root := t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), `{"adapter":"react","root_view":"Home"}`)
	ui := filepath.Join(root, "agentic", "ui", "Page.tsx")
	if err := os.MkdirAll(filepath.Dir(ui), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ui, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := sourceFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	// Build output, dependencies, traces and ordinary files do not matter.
	for _, ignored := range []string{"agentic/.spark/dist/ui/x.js", "agentic/node_modules/a/ui/b.js", "agentic/trace/ui/t.json", "agentic/code/main.py", "agentic/document/a.md"} {
		full := filepath.Join(root, filepath.FromSlash(ignored))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	if got, _ := sourceFingerprint(root); got != base {
		t.Fatal("an ignored file changed the fingerprint")
	}
	// Editing, adding or removing an interface file does.
	later := time.Now().Add(time.Hour)
	if err := os.WriteFile(ui, []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(ui, later, later)
	edited, _ := sourceFingerprint(root)
	if edited == base {
		t.Fatal("an edited interface did not change the fingerprint")
	}
	extra := filepath.Join(root, "agentic", "ui", "model", "More.ts")
	_ = os.MkdirAll(filepath.Dir(extra), 0o755)
	_ = os.WriteFile(extra, []byte("x"), 0o644)
	if added, _ := sourceFingerprint(root); added == edited {
		t.Fatal("a new interface file did not change the fingerprint")
	}
	// A changed context.json (its ui_config) does too.
	writeContext(t, filepath.Join(root, "agentic", "context.json"), `{"adapter":"react","root_view":"Other"}`)
	if withContext, _ := sourceFingerprint(root); withContext == edited {
		t.Fatal("a new context.json did not change the fingerprint")
	}
}

func TestPublicationRecordsAndPruning(t *testing.T) {
	data := t.TempDir()
	if _, ok := readPublicationRecord(data, "demo"); ok {
		t.Fatal("a record out of nothing")
	}
	record := PublicationRecord{PublicationID: "spub1_x", Fingerprint: "f", Directory: "d", At: "t"}
	if err := writePublicationRecord(data, "demo", record); err != nil {
		t.Fatal(err)
	}
	if got, ok := readPublicationRecord(data, "demo"); !ok || got.PublicationID != record.PublicationID || got.Fingerprint != record.Fingerprint {
		t.Fatalf("record: %+v %v", got, ok)
	}
	folder := publicationFolder(data, "demo")
	for _, name := range []string{"20260101T000000Z", "20260102T000000Z", "20260103T000000Z", "20260104T000000Z", "20260105T000000Z"} {
		if err := os.MkdirAll(filepath.Join(folder, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inUse := filepath.Join(folder, "20260101T000000Z") // the oldest, but still the one in use
	prunePublications(data, "demo", inUse)
	left, _ := os.ReadDir(folder)
	names := map[string]bool{}
	for _, entry := range left {
		names[entry.Name()] = true
	}
	if !names["20260101T000000Z"] || names["20260102T000000Z"] || !names["20260105T000000Z"] || !names[publicationRecord] {
		t.Fatalf("after pruning: %v", names)
	}
}

func TestPublishIsRefusedWithoutToolsAndWhileRunning(t *testing.T) {
	ui, token := diagnosisUI(t, nil)
	headers := map[string]string{"Content-Type": "application/json"}
	// Not registered: an error, not a job.
	if response, _ := webUIRequest(t, ui, "POST", "/api/instances/none/publish", token, headers, `{}`); response.StatusCode != 400 {
		t.Fatalf("unknown instance: %d", response.StatusCode)
	}
	instance := Instance{Alias: "demo", RootPath: t.TempDir()}
	if err := ui.startPublish(instance, false); err == nil {
		t.Fatal("publication started without tools")
	}
	ui.options.Publish = PublishTools{Available: true, repository: t.TempDir(), node: "/nonexistent-node"}
	ui.publisher.set("demo", "build", "running", "")
	if err := ui.startPublish(instance, false); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second publication started: %v", err)
	}
}

func TestPublicationStateComparesTheRecordAndTheSources(t *testing.T) {
	ui, _ := diagnosisUI(t, nil)
	root := t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), `{"adapter":"react","root_view":"Home"}`)
	instance := Instance{Alias: "demo", RootPath: root}
	if got, _ := ui.publicationState(instance); got != "none" {
		t.Fatalf("no publication: %s", got)
	}
	instance.PublicationID = "spub1_x"
	if got, _ := ui.publicationState(instance); got != "unknown" {
		t.Fatalf("a publication the launcher did not make: %s", got)
	}
	fingerprint, _ := sourceFingerprint(root)
	_ = writePublicationRecord(ui.options.DataDirectory, "demo", PublicationRecord{PublicationID: "spub1_x", Fingerprint: fingerprint})
	if got, _ := ui.publicationState(instance); got != "current" {
		t.Fatalf("unchanged sources: %s", got)
	}
	ui.publisher.forget(root)
	writeContext(t, filepath.Join(root, "context.json"), `{"adapter":"react","root_view":"Changed"}`)
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "context.json"), later, later)
	if got, _ := ui.publicationState(instance); got != "obsolete" {
		t.Fatalf("changed sources: %s", got)
	}
}

func TestDetectPublishToolsExplainsWhatIsMissing(t *testing.T) {
	tools := DetectPublishTools(filepath.Join(t.TempDir(), "bin", "brique-launcher"))
	if tools.Available || tools.Reason == "" {
		t.Fatalf("tools: %+v", tools)
	}
}

// Which interface changed: a file one interface imports from another folder counts for that
// interface only, an interface that appears or disappears is named, and an unchanged one is not.
func TestChangedInterfacesNamesExactlyTheInterfacesThatMoved(t *testing.T) {
	root := t.TempDir()
	web := `{"adapter":"react","root_view":"Home"}`
	editor := `{"adapter":"react","root_view":"BriqueEditorRoot"}`
	write := func(path, content string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rootContext := `{"brique":{"children_list":["alpha","beta"],"ui_config":` + editor + `}}`
	write(filepath.Join(root, "context.json"), rootContext)
	writeContext(t, filepath.Join(root, "alpha", "context.json"), web)
	writeContext(t, filepath.Join(root, "beta", "context.json"), web)
	alphaView := filepath.Join(root, "alpha", "ui", "View.tsx")
	betaView := filepath.Join(root, "beta", "ui", "View.tsx")
	shared := filepath.Join(root, "shared", "helpers.ts") // imported by beta from outside its own folder
	write(alphaView, "a1")
	write(betaView, "b1")
	write(shared, "s1")

	published := []InterfaceSnapshot{
		snapshotInterface("/root/alpha", "alpha", []string{alphaView}),
		snapshotInterface("/root/beta", "beta", []string{betaView, shared}),
	}
	if changed, err := changedInterfaces(root, published); err != nil || len(changed) != 0 {
		t.Fatalf("nothing moved: %v %v", changed, err)
	}

	later := time.Now().Add(time.Hour)
	touch := func(path, content string) {
		write(path, content)
		_ = os.Chtimes(path, later, later)
	}
	touch(shared, "s2 changed")
	if changed, _ := changedInterfaces(root, published); strings.Join(changed, ",") != "/root/beta" {
		t.Fatalf("a shared file must name only its importer: %v", changed)
	}
	touch(alphaView, "a2 changed")
	if changed, _ := changedInterfaces(root, published); strings.Join(changed, ",") != "/root/alpha,/root/beta" {
		t.Fatalf("both moved: %v", changed)
	}

	// A new interface is named, and so is one that is gone.
	fresh := t.TempDir()
	write(filepath.Join(fresh, "context.json"), `{"brique":{"children_list":["alpha","gamma"],"ui_config":`+editor+`}}`)
	writeContext(t, filepath.Join(fresh, "alpha", "context.json"), web)
	writeContext(t, filepath.Join(fresh, "gamma", "context.json"), web)
	write(filepath.Join(fresh, "alpha", "ui", "View.tsx"), "a1")
	original := []InterfaceSnapshot{snapshotInterface("/root/alpha", "alpha", []string{filepath.Join(fresh, "alpha", "ui", "View.tsx")}),
		snapshotInterface("/root/beta", "beta", nil)}
	if changed, _ := changedInterfaces(fresh, original); strings.Join(changed, ",") != "/root/beta,/root/gamma" {
		t.Fatalf("beta removed and gamma added: %v", changed)
	}
}

func TestPublishIsRefusedWhenNothingChangedUnlessForced(t *testing.T) {
	ui, _ := diagnosisUI(t, nil)
	root := t.TempDir()
	writeContext(t, filepath.Join(root, "context.json"), `{"adapter":"react","root_view":"Home"}`)
	view := filepath.Join(root, "ui", "Home.tsx")
	_ = os.MkdirAll(filepath.Dir(view), 0o755)
	_ = os.WriteFile(view, []byte("x"), 0o644)
	_ = writePublicationRecord(ui.options.DataDirectory, "demo", PublicationRecord{PublicationID: "spub1_x",
		Interfaces: []InterfaceSnapshot{snapshotInterface("/root", "main", []string{view})}})
	ui.options.Publish = PublishTools{Available: true, repository: t.TempDir(), node: "/nonexistent-node"}
	instance := Instance{Alias: "demo", RootPath: root, PublicationID: "spub1_x"}
	if state, changed := ui.publicationState(instance); state != "current" || len(changed) != 0 {
		t.Fatalf("state: %s %v", state, changed)
	}
	if err := ui.startPublish(instance, false); err == nil || !strings.Contains(err.Error(), "already up to date") {
		t.Fatalf("a publication of unchanged interfaces was started: %v", err)
	}
	if err := ui.startPublish(instance, true); err != nil {
		t.Fatalf("forced publication: %v", err)
	}
}
