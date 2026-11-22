// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The numeric settings of the wrapper runtime, read from the environment (BRIQUE_<NAME>).
//
// Every setting is declared here, once, with its default, its minimum and what an invalid value does: a strict setting stops the wrapper at
// start, a tolerant one ignores the value and uses the default. The declarations are the same in the three runtimes: vectors/settings.json
// holds them with what each one is for, every runtime's test compares its own to it, and the table of the settings is generated from that
// file. DescribeSettings says what a running wrapper really uses, so that a setting that did not take effect shows.

// Setting declares one numeric setting.
type Setting struct {
	Name    string
	Default int
	Minimum int
	Strict  bool
	What    string // what it sets, for a table; the shared file holds it for the built-in ones
}

// Settings are the declared settings, in the order of the shared file.
var Settings = []Setting{
	{"BRIQUE_MAX_CONCURRENCY", 16, 1, true, ""},
	{"BRIQUE_QUEUE_LIMIT", 256, 1, true, ""},
	{"BRIQUE_TRANSIT_MAX_BYTES", 64 * 1024 * 1024, 1, true, ""},
	{"BRIQUE_TRANSIT_TTL_SECONDS", 60 * 60, 1, true, ""},
	{"BRIQUE_BUSY_ATTEMPTS", 8, 1, false, ""},
	{"BRIQUE_BUSY_BASE_MS", 20, 0, false, ""},
	{"BRIQUE_BUSY_CAP_MS", 1000, 0, false, ""},
	{"BRIQUE_LEASE_TIMEOUT_MS", 30000, 1, false, ""},
	{"BRIQUE_LEASE_RETRIES", 5, 1, false, ""},
	{"BRIQUE_CAS_ATTEMPTS", 12, 1, false, ""},
	{"BRIQUE_CAS_PAUSE_BASE_MS", 2, 0, false, ""},
	{"BRIQUE_CAS_PAUSE_CAP_MS", 50, 0, false, ""},
	{"BRIQUE_HEARTBEAT_MS", 10000, 50, false, ""},
}

// DeclareSetting declares an integer setting of hosted code, so that it shows in DescribeSettings, in the log of the start and in the diagnostic
// like the built-in ones, and returns its value. Declaring a name again keeps the first declaration.
func DeclareSetting(name string, defaultValue, minimum int, what string) int {
	for _, setting := range Settings {
		if setting.Name == name {
			return settingInt(name)
		}
	}
	Settings = append(Settings, Setting{Name: name, Default: defaultValue, Minimum: minimum, What: what})
	return settingInt(name)
}

func declared(name string) Setting {
	for _, setting := range Settings {
		if setting.Name == name {
			return setting
		}
	}
	panic("undeclared setting " + name)
}

// readSetting returns the value, where it comes from ("default", "env" or "ignored": a value was given and could not be used) and, for a
// strict setting, an error when the value given is invalid.
func readSetting(setting Setting) (value int, source string, err error) {
	raw := strings.TrimSpace(os.Getenv(setting.Name))
	if raw == "" {
		return setting.Default, "default", nil
	}
	if number, parseErr := strconv.Atoi(raw); parseErr == nil && number >= setting.Minimum {
		return number, "env", nil
	}
	if setting.Strict {
		if setting.Minimum == 1 {
			return setting.Default, "invalid", fmt.Errorf("%s must be a positive integer", setting.Name)
		}
		return setting.Default, "invalid", fmt.Errorf("%s must be an integer of %d or more", setting.Name, setting.Minimum)
	}
	return setting.Default, "ignored", nil
}

// settingInt is the value of a declared setting; a tolerant one never fails, a strict one with an invalid value gives its default (use
// strictSetting where the wrapper must stop).
func settingInt(name string) int {
	value, _, _ := readSetting(declared(name))
	return value
}

// strictSetting is the value of a declared setting, and an error when the value given is invalid.
func strictSetting(name string) (int, error) {
	value, _, err := readSetting(declared(name))
	return value, err
}

// SettingState is what the running process uses for one setting.
type SettingState struct {
	Setting
	Value  int
	Source string // "default", "env", "ignored" or "invalid"
}

// DescribeSettings says what the running process uses for each setting, and where it comes from.
func DescribeSettings() []SettingState {
	states := make([]SettingState, 0, len(Settings))
	for _, setting := range Settings {
		value, source, _ := readSetting(setting)
		states = append(states, SettingState{Setting: setting, Value: value, Source: source})
	}
	return states
}

// SettingsNotDefault lists the settings that are not at their default, for the log of the start and the diagnostic.
func SettingsNotDefault() map[string]int {
	changed := map[string]int{}
	for _, state := range DescribeSettings() {
		if state.Source != "default" && state.Value != state.Default {
			changed[state.Name] = state.Value
		}
	}
	return changed
}
