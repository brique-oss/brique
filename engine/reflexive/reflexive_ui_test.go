/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package reflexive

import (
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestActiveWebUIsUsesOnlyActiveCompatibleContexts(t *testing.T) {
	root := t.TempDir()
	writeUIContext(t, root, "RootView", "react", "home")
	writeUIContext(t, filepath.Join(root, "app"), "AppView", "react", "app")
	writeUIContext(t, filepath.Join(root, "editor"), "BriqueEditorRoot", "react", "editor")
	writeUIContext(t, filepath.Join(root, "inactive"), "HiddenView", "react", "hidden")

	registry := junction.NewContextCommRegistry()
	channel := make(chan junction.Delivery)
	registry.Register(shared.ContextAddr("/root"), channel, "root")
	registry.Register(shared.ContextAddr("/root/app"), channel, "app")
	registry.Register(shared.ContextAddr("/root/editor"), channel, "editor")
	if err := registry.InstallPublishedUIInventory("spub1_test", []junction.PublishedUI{
		{ContextPath: "/root", UIName: "home", URL: "/"},
		{ContextPath: "/root/app", UIName: "app", URL: "/ui/ui1_kDFHYoF0cvylLgnOULOHPjvEMzAAYAyOY8HBDmqcp2Y/"},
		{ContextPath: "/root/editor", UIName: "editor", URL: webUIURL("/root/editor", "editor")},
		{ContextPath: "/root/inactive", UIName: "hidden", URL: webUIURL("/root/inactive", "hidden")},
	}); err != nil {
		t.Fatal(err)
	}
	loop := NewReflexiveLoop(&junction.ContextRegistry{CtxId: "/root", ContextDir: root, CtxCommReg: registry}, nil)
	entries, err := loop.activeWebUIs()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ContextPath != "/root" || entries[0].URL != "/" ||
		entries[1].ContextPath != "/root/app" || entries[1].UIName != "app" {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[1].URL != "/ui/ui1_kDFHYoF0cvylLgnOULOHPjvEMzAAYAyOY8HBDmqcp2Y/" {
		t.Fatalf("Go/TypeScript UI route vector diverged: %q", entries[1].URL)
	}
}

func TestUIOpenExtractorBindsTheRequestedContext(t *testing.T) {
	registry, err := buildReflexiveAccessRegistry()
	if err != nil {
		t.Fatal(err)
	}
	in := circulation.Intention{To: circulation.Address{Context: "/root", Type: "reflexive", Cap: "ui.open"}, Params: map[string]any{
		uiContextPathParam: "/root/app", uiNameParam: "app",
	}}
	accesses, err := registry.Extract("ui.open", "/root", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(accesses) != 1 || accesses[0].Context != "/root/app" || accesses[0].Kind != "context" || accesses[0].Action != "read" {
		t.Fatalf("accesses = %#v", accesses)
	}
	in.Params[uiContextPathParam] = "../secret"
	if _, err := registry.Extract("ui.open", "/root", in); err == nil {
		t.Fatal("non-canonical UI context was accepted")
	}
}

func TestUIListAndOpenHandlersReturnOnlyPublishedActiveCoordinates(t *testing.T) {
	root := t.TempDir()
	writeUIContext(t, root, "RootView", "react", "home")
	writeUIContext(t, filepath.Join(root, "app"), "AppView", "react", "app")
	registry := junction.NewContextCommRegistry()
	contextChannel := make(chan junction.Delivery)
	registry.Register("/root", contextChannel, "root")
	registry.Register("/root/app", contextChannel, "app")
	appURL := webUIURL("/root/app", "app")
	if err := registry.InstallPublishedUIInventory("spub1_test", []junction.PublishedUI{
		{ContextPath: "/root", UIName: "home", URL: "/"},
		{ContextPath: "/root/app", UIName: "app", URL: appURL},
	}); err != nil {
		t.Fatal(err)
	}
	responses := make(chan circulation.Message, 4)
	loop := NewReflexiveLoop(&junction.ContextRegistry{
		CtxId: "/root", ContextDir: root, CtxCommReg: registry,
		FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: reflexiveDeliveryBridge(responses, 4)},
	}, nil)

	list := circulation.Intention{IntentionID: "ui-list", From: circulation.Address{Context: "/root/caller"},
		To: circulation.Address{Context: "/root", Type: "reflexive", Cap: "ui.list"}}
	loop.capUIList(circulation.Message{Kind: circulation.ValueKindIntention, Intention: list})
	listed := recvReflexiveLoopMsg(t, responses, "ui.list")
	if listed.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("ui.list = %#v", listed.Response)
	}
	items, ok := listed.Response.Payload[circulation.KeyResult].([]webUIEntry)
	if !ok || len(items) != 2 || items[1].URL != appURL {
		t.Fatalf("ui.list payload = %#v", listed.Response.Payload)
	}

	open := list
	open.IntentionID, open.To.Cap = "ui-open", "ui.open"
	open.Params = map[string]any{uiContextPathParam: "/root/app", uiNameParam: "app"}
	loop.capUIOpen(circulation.Message{Kind: circulation.ValueKindIntention, Intention: open})
	opened := recvReflexiveLoopMsg(t, responses, "ui.open")
	if opened.Response.Status != circulation.ValueStatusOK || opened.Response.Payload[uiURLField] != appURL {
		t.Fatalf("ui.open = %#v", opened.Response)
	}
}

func TestActiveContextIDsAreSortedSnapshots(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	channel := make(chan junction.Delivery)
	registry.Register("/root/z", channel, "z")
	registry.Register("/root", channel, "root")
	registry.Register("/root/a", channel, "a")
	got := registry.ActiveContextIDs()
	want := []shared.ContextAddr{"/root", "/root/a", "/root/z"}
	if len(got) != len(want) {
		t.Fatalf("ids = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ids = %#v", got)
		}
	}
}

func writeUIContext(t *testing.T, directory, rootView, adapter, name string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"brique":{"ui_config":{"root_view":"` + rootView + `","adapter":"` + adapter + `","host":"web","name":"` + name + `"}}}`)
	if err := os.WriteFile(filepath.Join(directory, ContextDescriptorFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}
}
