/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

#pragma once

// The numeric settings of the wrapper runtime, read from the environment (BRIQUE_<NAME>).
//
// Every setting is declared here, once, with its default, its minimum and what an invalid value does: a strict setting stops the wrapper at
// start, a tolerant one ignores the value and uses the default. The declarations are the same in the three runtimes: vectors/settings.json
// holds them with what each one is for, every runtime's test compares its own to it, and the table of the settings is generated from that
// file. describe_settings says what a running wrapper really uses, so that a setting that did not take effect shows.

#include <cstdlib>
#include <map>
#include <stdexcept>
#include <string>
#include <vector>

namespace wrapper {

struct Setting {
    std::string name;
    long long default_value;
    long long minimum;
    bool strict;
    std::string what = "";   // what it sets, for a table; the shared file holds it for the built-in ones
};

inline std::vector<Setting>& declared_settings() {
    static std::vector<Setting> settings = {
        {"BRIQUE_MAX_CONCURRENCY", 16, 1, true},
        {"BRIQUE_QUEUE_LIMIT", 256, 1, true},
        {"BRIQUE_TRANSIT_MAX_BYTES", 64LL * 1024 * 1024, 1, true},
        {"BRIQUE_TRANSIT_TTL_SECONDS", 60 * 60, 1, true},
        {"BRIQUE_BUSY_ATTEMPTS", 8, 1, false},
        {"BRIQUE_BUSY_BASE_MS", 20, 0, false},
        {"BRIQUE_BUSY_CAP_MS", 1000, 0, false},
        {"BRIQUE_LEASE_TIMEOUT_MS", 30000, 1, false},
        {"BRIQUE_LEASE_RETRIES", 5, 1, false},
        {"BRIQUE_CAS_ATTEMPTS", 12, 1, false},
        {"BRIQUE_CAS_PAUSE_BASE_MS", 2, 0, false},
        {"BRIQUE_CAS_PAUSE_CAP_MS", 50, 0, false},
        {"BRIQUE_HEARTBEAT_MS", 10000, 50, false},
    };
    return settings;
}

// Declare an integer setting of hosted code, so that it shows in describe_settings, in the log of the start and in the diagnostic like the
// built-in ones. Declaring a name again keeps the first declaration. Declare at the start of the process, before threads read the settings.
inline void declare_setting(const std::string& name, long long default_value, long long minimum, const std::string& what = "") {
    for (const Setting& setting : declared_settings()) if (setting.name == name) return;
    declared_settings().push_back(Setting{name, default_value, minimum, false, what});
}

inline const Setting& declared_setting(const std::string& name) {
    for (const Setting& setting : declared_settings()) if (setting.name == name) return setting;
    throw std::logic_error("undeclared setting " + name);
}

struct SettingState {
    Setting setting;
    long long value = 0;
    std::string source;   // "default", "env", "ignored" or "invalid"
};

inline SettingState read_setting(const Setting& setting) {
    const char* raw_env = std::getenv(setting.name.c_str());
    std::string raw = raw_env == nullptr ? "" : raw_env;
    const std::size_t first = raw.find_first_not_of(" \t\r\n");
    raw = first == std::string::npos ? "" : raw.substr(first, raw.find_last_not_of(" \t\r\n") - first + 1);
    if (raw.empty()) return {setting, setting.default_value, "default"};
    try {
        std::size_t used = 0;
        const long long number = std::stoll(raw, &used);
        if (used == raw.size() && number >= setting.minimum) return {setting, number, "env"};
    } catch (...) {
    }
    return {setting, setting.default_value, setting.strict ? "invalid" : "ignored"};
}

// The value of a declared setting; a strict one with an invalid value gives its default (use strict_setting where the wrapper must stop).
inline long long setting_value(const std::string& name) { return read_setting(declared_setting(name)).value; }

// The value of a declared setting; std::runtime_error when the value given is invalid.
inline long long strict_setting(const std::string& name) {
    const SettingState state = read_setting(declared_setting(name));
    if (state.source == "invalid") {
        throw std::runtime_error(name + (state.setting.minimum == 1 ? " must be a positive integer"
                                                                    : " must be an integer of " + std::to_string(state.setting.minimum) + " or more"));
    }
    return state.value;
}

inline std::vector<SettingState> describe_settings() {
    std::vector<SettingState> states;
    for (const Setting& setting : declared_settings()) states.push_back(read_setting(setting));
    return states;
}

// The settings that are not at their default, for the log of the start and the diagnostic.
inline std::map<std::string, long long> settings_not_default() {
    std::map<std::string, long long> changed;
    for (const SettingState& state : describe_settings()) {
        if (state.source != "default" && state.value != state.setting.default_value) changed[state.setting.name] = state.value;
    }
    return changed;
}

}  // namespace wrapper
