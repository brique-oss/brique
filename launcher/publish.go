/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	editorRootView       = "BriqueEditorRoot"
	publicationsDirName  = "publications"
	publicationRecord    = "last.json"
	keptPublications     = 3
	maxFingerprintFiles  = 50000
	fingerprintCacheTime = 15 * time.Second
)

// webRootFor decides which context is the page a visitor lands on. An instance whose root UI is a
// web UI uses it (no argument). An instance whose root UI is the editor (never published) must have a
// child context named "web" with its own UI: it is named with --web-root and every other UI of the
// tree is published next to it. Anything else cannot serve a web page.
func webRootFor(rootPath string) (argument string, err error) {
	root, err := readContextUI(filepath.Join(rootPath, "context.json"))
	if err != nil {
		return "", err
	}
	if root.hasUI && root.rootView != editorRootView && root.adapter == "react" {
		return "", nil
	}
	if !root.hasUI || root.rootView == editorRootView {
		child, err := readContextUI(filepath.Join(rootPath, "web", "context.json"))
		if err == nil && child.hasUI && child.rootView != editorRootView && child.adapter == "react" {
			return "/root/web", nil
		}
		return "", errors.New("the instance has no web home page: add a context named \"web\" with a React UI")
	}
	return "", errors.New("the root UI of the instance cannot be published on the web")
}

type contextUI struct {
	hasUI    bool
	rootView string
	adapter  string
}

func readContextUI(path string) (contextUI, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return contextUI{}, err
	}
	var descriptor struct {
		Brique struct {
			UIConfig *struct {
				RootView string `json:"root_view"`
				Adapter  string `json:"adapter"`
			} `json:"ui_config"`
		} `json:"brique"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return contextUI{}, fmt.Errorf("%s: %w", path, err)
	}
	if descriptor.Brique.UIConfig == nil {
		return contextUI{}, nil
	}
	return contextUI{hasUI: true, rootView: descriptor.Brique.UIConfig.RootView, adapter: descriptor.Brique.UIConfig.Adapter}, nil
}

// sourceFingerprint summarises what a publication was built from: every context.json and every file
// under a folder named "ui" in the instance, by path, size and modification time. It changes when an
// interface is added, edited or removed, so the page can say a publication is out of date. Build
// output, dependencies and data folders are skipped.
func sourceFingerprint(rootPath string) (string, error) {
	skipped := map[string]bool{".spark": true, "node_modules": true, ".git": true, "trace": true, "tmp": true, "build": true, "matter": true}
	type entry struct{ path, detail string }
	var entries []entry
	err := filepath.WalkDir(rootPath, func(path string, dirEntry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return relErr
		}
		if dirEntry.IsDir() {
			if relative != "." && skipped[dirEntry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !dirEntry.Type().IsRegular() {
			return nil
		}
		inUI := false
		for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(relative)), "/") {
			if part == "ui" {
				inUI = true
			}
		}
		if !inUI && dirEntry.Name() != "context.json" {
			return nil
		}
		info, infoErr := dirEntry.Info()
		if infoErr != nil {
			return infoErr
		}
		entries = append(entries, entry{filepath.ToSlash(relative), fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())})
		if len(entries) > maxFingerprintFiles {
			return errors.New("too many files to fingerprint")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(hash, "%d:%s\n%s\n", len(e.path), e.path, e.detail)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// PublicationRecord is what the launcher keeps about the last publication it made for an instance.
type PublicationRecord struct {
	PublicationID string `json:"publication_id"`
	Fingerprint   string `json:"fingerprint"`
	Directory     string `json:"directory"`
	At            string `json:"at"`
	// Interfaces says, for each published interface, which files it was built from and what they
	// looked like. It lets the page name the interfaces that changed since, instead of "something".
	Interfaces []InterfaceSnapshot `json:"interfaces,omitempty"`
}

// InterfaceSnapshot is one published interface and the state of its source files at publication.
type InterfaceSnapshot struct {
	ContextPath string `json:"context_path"`
	UIName      string `json:"ui_name"`
	// Files maps an absolute source path to "size:modification time".
	Files map[string]string `json:"files"`
}

func fileStamp(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()), true
}

// snapshotInterface records the source files of an interface as they are now.
func snapshotInterface(contextPath, uiName string, files []string) InterfaceSnapshot {
	snapshot := InterfaceSnapshot{ContextPath: contextPath, UIName: uiName, Files: map[string]string{}}
	for _, file := range files {
		if stamp, ok := fileStamp(file); ok {
			snapshot.Files[file] = stamp
		}
	}
	return snapshot
}

// discoverUIContexts lists the contexts of the instance that have a web-publishable React UI, by
// their logical path, following children_list from the root like the publication script does.
func discoverUIContexts(rootPath string) (map[string]bool, error) {
	found := map[string]bool{}
	var visit func(directory, logical string, depth int) error
	visit = func(directory, logical string, depth int) error {
		if depth > 32 {
			return errors.New("context tree too deep")
		}
		raw, err := os.ReadFile(filepath.Join(directory, "context.json"))
		if err != nil {
			return err
		}
		var descriptor struct {
			Brique struct {
				Children []string `json:"children_list"`
				UIConfig *struct {
					RootView string `json:"root_view"`
					Adapter  string `json:"adapter"`
				} `json:"ui_config"`
			} `json:"brique"`
		}
		if err := json.Unmarshal(raw, &descriptor); err != nil {
			return err
		}
		if ui := descriptor.Brique.UIConfig; ui != nil && ui.Adapter == "react" && ui.RootView != editorRootView {
			found[logical] = true
		}
		for _, child := range descriptor.Brique.Children {
			if child == "" || child == "." || child == ".." || strings.ContainsAny(child, `/\`) {
				continue
			}
			if err := visit(filepath.Join(directory, child), logical+"/"+child, depth+1); err != nil {
				continue // a child that cannot be read is not a published interface
			}
		}
		return nil
	}
	if err := visit(rootPath, "/root", 0); err != nil {
		return nil, err
	}
	return found, nil
}

// changedInterfaces names the interfaces that differ from a publication: a source file edited or
// removed, an interface added to the instance, or one that is no longer there. Names are logical
// context paths, sorted.
func changedInterfaces(rootPath string, interfaces []InterfaceSnapshot) ([]string, error) {
	discovered, err := discoverUIContexts(rootPath)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	recorded := map[string]bool{}
	for _, snapshot := range interfaces {
		recorded[snapshot.ContextPath] = true
		if !discovered[snapshot.ContextPath] {
			changed[snapshot.ContextPath] = true // removed, or no longer a web interface
			continue
		}
		for file, stamp := range snapshot.Files {
			if now, ok := fileStamp(file); !ok || now != stamp {
				changed[snapshot.ContextPath] = true
				break
			}
		}
	}
	for contextPath := range discovered {
		if !recorded[contextPath] {
			changed[contextPath] = true // added
		}
	}
	names := make([]string, 0, len(changed))
	for name := range changed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func publicationFolder(dataDirectory, alias string) string {
	return filepath.Join(dataDirectory, publicationsDirName, alias)
}

func readPublicationRecord(dataDirectory, alias string) (PublicationRecord, bool) {
	raw, err := os.ReadFile(filepath.Join(publicationFolder(dataDirectory, alias), publicationRecord))
	if err != nil {
		return PublicationRecord{}, false
	}
	var record PublicationRecord
	if json.Unmarshal(raw, &record) != nil || record.PublicationID == "" {
		return PublicationRecord{}, false
	}
	return record, true
}

func writePublicationRecord(dataDirectory, alias string, record PublicationRecord) error {
	folder := publicationFolder(dataDirectory, alias)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary := filepath.Join(folder, publicationRecord+".tmp")
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(folder, publicationRecord))
}

// prunePublications removes the oldest publication folders of an instance, never the one in use
// and keeping the newest few.
func prunePublications(dataDirectory, alias, inUse string) {
	folder := publicationFolder(dataDirectory, alias)
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names) // names are build times: oldest first
	for len(names) > keptPublications {
		oldest := names[0]
		names = names[1:]
		if filepath.Join(folder, oldest) == inUse {
			continue
		}
		_ = os.RemoveAll(filepath.Join(folder, oldest))
	}
}
