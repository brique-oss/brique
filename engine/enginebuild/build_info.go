/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package enginebuild

import (
	"runtime"
	"runtime/debug"
	"strconv"

	"brique_engine/contracts/buildinfo"
	"brique_engine/engine/shared"
)

// These values may be set by -ldflags. When absent, Go's embedded VCS settings
// are used. A development build is allowed to report no revision; a packager
// can reject that later without making the engine lie about its provenance.
var (
	EngineBuildRevision = ""
	EngineBuildModified = ""
	EngineBuildTime     = ""
)

func CurrentBuildInfo() buildinfo.Info {
	result := buildinfo.Info{
		Format: buildinfo.Format, EngineVersion: shared.EngineBinaryVersion,
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
