/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package shared

import (
	"runtime"
	"runtime/debug"
	"strconv"
)

const BuildInfoFormat = "brique.build-info/1"

// These values may be set by -ldflags. When absent, Go's embedded VCS settings
// are used. A development build is allowed to report no revision; a packager
// can reject that later without making the engine lie about its provenance.
var (
	EngineBuildRevision = ""
	EngineBuildModified = ""
	EngineBuildTime     = ""
)

type BuildInfo struct {
	Format        string `json:"format"`
	EngineVersion string `json:"engine_version"`
	VCSRevision   string `json:"vcs_revision,omitempty"`
	VCSModified   *bool  `json:"vcs_modified,omitempty"`
	VCSTime       string `json:"vcs_time,omitempty"`
	GoVersion     string `json:"go_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

func CurrentBuildInfo() BuildInfo {
	result := BuildInfo{
		Format: BuildInfoFormat, EngineVersion: EngineBinaryVersion,
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		VCSRevision: EngineBuildRevision, VCSTime: EngineBuildTime,
	}
	if modified, err := strconv.ParseBool(EngineBuildModified); err == nil {
		result.VCSModified = &modified
	}
	if embedded, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range embedded.Settings {
			switch setting.Key {
			case "vcs.revision":
				if result.VCSRevision == "" {
					result.VCSRevision = setting.Value
				}
			case "vcs.modified":
				if result.VCSModified == nil {
					if modified, err := strconv.ParseBool(setting.Value); err == nil {
						result.VCSModified = &modified
					}
				}
			case "vcs.time":
				if result.VCSTime == "" {
					result.VCSTime = setting.Value
				}
			}
		}
	}
	return result
}

func (info BuildInfo) Valid() bool {
	return info.Format == BuildInfoFormat && info.EngineVersion != "" && info.GoVersion != "" && info.OS != "" && info.Arch != ""
}
