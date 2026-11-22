// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"encoding/json"
	"os"
	"testing"
)

type declaredSetting struct {
	Name    string `json:"name"`
	Default int    `json:"default"`
	Minimum int    `json:"minimum"`
	Unit    string `json:"unit"`
	Policy  string `json:"policy"`
	What    string `json:"what"`
}

func TestSettingsAreTheSharedDeclarations(t *testing.T) {
	raw, err := os.ReadFile("../../../vectors/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Settings []declaredSetting `json:"settings"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Settings) > len(Settings) {
		t.Fatalf("%d settings declared, %d in the shared file", len(Settings), len(file.Settings))
	}
	for i, want := range file.Settings {
		got := Settings[i]
		if got.Name != want.Name || got.Default != want.Default || got.Minimum != want.Minimum || got.Strict != (want.Policy == "strict") || want.What == "" || want.Unit == "" {
			t.Errorf("setting %d: %+v, shared file says %+v", i, got, want)
		}
	}
}

func TestSettingsValuesAndSources(t *testing.T) {
	for _, setting := range Settings {
		t.Setenv(setting.Name, "")
	}
	if got := settingInt("BRIQUE_BUSY_ATTEMPTS"); got != 8 || len(SettingsNotDefault()) != 0 {
		t.Fatalf("defaults: %d %v", got, SettingsNotDefault())
	}
	for _, bad := range []string{"abc", "1.5", "-3", "0"} { // 0 is below the minimum of 1
		t.Setenv("BRIQUE_BUSY_ATTEMPTS", bad)
		if got := settingInt("BRIQUE_BUSY_ATTEMPTS"); got != 8 {
			t.Errorf("tolerant setting with %q: %d, want the default", bad, got)
		}
	}
	t.Setenv("BRIQUE_BUSY_BASE_MS", "0") // 0 is allowed for this one
	if got := settingInt("BRIQUE_BUSY_BASE_MS"); got != 0 {
		t.Errorf("zero must be accepted, got %d", got)
	}
	for _, bad := range []string{"abc", "0", "-1"} {
		t.Setenv("BRIQUE_MAX_CONCURRENCY", bad)
		if _, err := strictSetting("BRIQUE_MAX_CONCURRENCY"); err == nil {
			t.Errorf("strict setting with %q must fail", bad)
		}
	}
	t.Setenv("BRIQUE_MAX_CONCURRENCY", "")
	t.Setenv("BRIQUE_CAS_ATTEMPTS", "3")
	t.Setenv("BRIQUE_LEASE_RETRIES", "nope")
	sources := map[string]string{}
	for _, state := range DescribeSettings() {
		sources[state.Name] = state.Source
	}
	if sources["BRIQUE_CAS_ATTEMPTS"] != "env" || sources["BRIQUE_LEASE_RETRIES"] != "ignored" || sources["BRIQUE_BUSY_CAP_MS"] != "default" {
		t.Errorf("sources: %v", sources)
	}
	if changed := SettingsNotDefault(); len(changed) != 2 || changed["BRIQUE_CAS_ATTEMPTS"] != 3 || changed["BRIQUE_BUSY_BASE_MS"] != 0 {
		t.Errorf("not default: %v", changed)
	}
}

func TestHostedCodeDeclaresItsOwnSettings(t *testing.T) {
	t.Setenv("BRIQUE_TEST_COUNT", "7")
	if got := DeclareSetting("BRIQUE_TEST_COUNT", 4, 1, "a count"); got != 7 {
		t.Fatalf("declared setting: %d", got)
	}
	if got := DeclareSetting("BRIQUE_TEST_COUNT", 9, 0, "again"); got != 7 || declared("BRIQUE_TEST_COUNT").Default != 4 {
		t.Fatalf("declaring again must keep the first declaration, got %d / %+v", got, declared("BRIQUE_TEST_COUNT"))
	}
	if SettingsNotDefault()["BRIQUE_TEST_COUNT"] != 7 {
		t.Fatalf("a declared setting must show in the settings not at their default: %v", SettingsNotDefault())
	}
}
