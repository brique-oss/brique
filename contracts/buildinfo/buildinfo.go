/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package buildinfo

// Format is the versioned wire name of the build-information contract.
const Format = "brique.build-info/1"

// Info is the build information an engine reports about itself.
type Info struct {
	Format        string `json:"format"`
	EngineVersion string `json:"engine_version"`
	VCSRevision   string `json:"vcs_revision,omitempty"`
	VCSModified   *bool  `json:"vcs_modified,omitempty"`
	VCSTime       string `json:"vcs_time,omitempty"`
	GoVersion     string `json:"go_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

func (info Info) Valid() bool {
	return info.Format == Format && info.EngineVersion != "" && info.GoVersion != "" && info.OS != "" && info.Arch != ""
}
