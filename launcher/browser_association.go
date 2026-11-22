/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"brique_engine/contracts/instanceselection"
	"brique_engine/platform/protectedfile"
)

const (
	BrowserAssociationsFormat   = "brique.browser-associations/1"
	browserAssociationsRelative = "browser/associations.json"
	browserAssociationsLock     = "browser/associations.lock"
	maxBrowserAssociationsBytes = 64 << 10
)

// BrowserAssociation is what the owner chose for the extension of one browser: the instances it
// vouches for (a page served by one of them is admitted as local.ui) and the one instance that
// carries the person's identity and signs for pages of instances that are not local. Each browser
// loads its own copy of the extension, so it has its own extension identifier.
type BrowserAssociation struct {
	Browser     string   `json:"browser"`
	ExtensionID string   `json:"extension_id"`
	Instances   []string `json:"instances"`
	Identity    string   `json:"identity"`
}

type BrowserAssociations struct {
	Format   string               `json:"format"`
	Revision uint64               `json:"revision"`
	Entries  []BrowserAssociation `json:"entries"`
}

func (a BrowserAssociations) forBrowser(browser string) (BrowserAssociation, bool) {
	for _, entry := range a.Entries {
		if entry.Browser == browser {
			return entry, true
		}
	}
	return BrowserAssociation{}, false
}

// BrowserAssociations returns what is associated; none is a valid state, not an error.
func (s Store) BrowserAssociations() (BrowserAssociations, error) {
	directory, err := s.browserDirectory()
	if err != nil {
		return BrowserAssociations{}, err
	}
	return s.readBrowserAssociations(directory)
}

// BrowserAssociationFor finds the association of the extension that started a native host.
func (s Store) BrowserAssociationFor(extensionID string) (BrowserAssociation, uint64, error) {
	associations, err := s.BrowserAssociations()
	if err != nil {
		return BrowserAssociation{}, 0, err
	}
	for _, entry := range associations.Entries {
		if entry.ExtensionID == extensionID {
			return entry, associations.Revision, nil
		}
	}
	return BrowserAssociation{}, 0, errors.New("no instance is associated with this extension")
}

// browserExtensionID is the identifier the extension of a browser has once loaded from its folder.
func browserExtensionID(dataDirectory, browser string) string {
	if browser == "firefox" {
		return FirefoxExtensionID
	}
	return ExpectedChromiumExtensionID(extensionFolder(dataDirectory, browser))
}

// SetBrowserAssociation associates instances with the extension of a browser and says which of them
// carries the identity (the first when none is named). It prepares the extension folder of that
// browser from the identity instance's runtime and registers the native host for every associated
// extension, so that nothing else has to be done but loading the extension in the browser.
func (s Store) SetBrowserAssociation(ctx context.Context, browser string, instances []string, identity string) (BrowserAssociation, error) {
	if !supportedBrowser(browser) {
		return BrowserAssociation{}, errors.New("unsupported browser")
	}
	unique := slices.Clone(instances)
	sort.Strings(unique)
	unique = slices.Compact(unique)
	if len(unique) == 0 {
		return BrowserAssociation{}, errors.New("choose at least one instance")
	}
	if identity == "" {
		identity = unique[0]
	}
	if !slices.Contains(unique, identity) {
		return BrowserAssociation{}, errors.New("the identity instance must be one of the associated instances")
	}
	for _, alias := range unique {
		instance, err := s.Get(alias)
		if err != nil {
			return BrowserAssociation{}, err
		}
		if instance.RuntimePath == "" {
			return BrowserAssociation{}, fmt.Errorf("instance %q runs from a bare engine: the browser bridge comes with the runtime package", alias)
		}
	}
	identityInstance, _ := s.Get(identity)
	if err := PrepareBrowserExtension(s.Directory, identityInstance.RuntimePath, browser); err != nil {
		return BrowserAssociation{}, err
	}
	entry := BrowserAssociation{Browser: browser, ExtensionID: browserExtensionID(s.Directory, browser), Instances: unique, Identity: identity}
	if err := s.updateBrowserAssociations(func(associations *BrowserAssociations) {
		associations.Entries = slices.DeleteFunc(associations.Entries, func(existing BrowserAssociation) bool { return existing.Browser == browser })
		associations.Entries = append(associations.Entries, entry)
	}); err != nil {
		return BrowserAssociation{}, err
	}
	if err := s.syncBrowserNativeHosts(ctx); err != nil {
		return BrowserAssociation{}, err
	}
	return entry, nil
}

// ClearBrowserAssociation removes the association of a browser and its native host registration. The
// extension itself stays in the browser (only the browser can remove it) but reaches no instance.
func (s Store) ClearBrowserAssociation(ctx context.Context, browser string) error {
	if !supportedBrowser(browser) {
		return errors.New("unsupported browser")
	}
	if err := s.updateBrowserAssociations(func(associations *BrowserAssociations) {
		associations.Entries = slices.DeleteFunc(associations.Entries, func(existing BrowserAssociation) bool { return existing.Browser == browser })
	}); err != nil {
		return err
	}
	return s.syncBrowserNativeHosts(ctx)
}

func (s Store) updateBrowserAssociations(change func(*BrowserAssociations)) error {
	directory, err := s.browserDirectory()
	if err != nil {
		return err
	}
	lock, err := protectedfile.OpenProtectedFile(filepath.Join(directory, filepath.FromSlash(browserAssociationsLock)), true, nil, nil)
	if err != nil {
		return err
	}
	defer lock.Close()
	locked, err := protectedfile.LockFile(lock, true)
	if err != nil || !locked {
		return errors.New("browser associations lock unavailable")
	}
	defer protectedfile.UnlockFile(lock)
	current, err := s.readBrowserAssociations(directory)
	if err != nil {
		return err
	}
	next := BrowserAssociations{Format: BrowserAssociationsFormat, Revision: current.Revision + 1, Entries: slices.Clone(current.Entries)}
	change(&next)
	sort.Slice(next.Entries, func(i, j int) bool { return next.Entries[i].Browser < next.Entries[j].Browser })
	raw, err := json.Marshal(next)
	if err != nil {
		return errors.New("encode browser associations")
	}
	return protectedfile.WriteProtectedFile(directory, browserAssociationsRelative, append(raw, '\n'), 0o600)
}

func (s Store) browserDirectory() (string, error) {
	if s.Directory == "" {
		return "", errors.New("launcher data directory is required")
	}
	directory, err := filepath.Abs(s.Directory)
	if err != nil {
		return "", errors.New("invalid launcher data directory")
	}
	if err := protectedfile.CreateProtectedDirectory(directory); err != nil {
		return "", err
	}
	if err := protectedfile.CreateProtectedDirectory(filepath.Join(directory, "browser")); err != nil {
		return "", err
	}
	if err := protectedfile.ValidateProtectedDirectory(directory, nil); err != nil {
		return "", err
	}
	return directory, nil
}

func (s Store) readBrowserAssociations(directory string) (BrowserAssociations, error) {
	path := filepath.Join(directory, filepath.FromSlash(browserAssociationsRelative))
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return BrowserAssociations{Format: BrowserAssociationsFormat}, nil
	} else if err != nil {
		return BrowserAssociations{}, errors.New("browser associations unavailable")
	}
	raw, err := protectedfile.ReadProtectedFile(directory, browserAssociationsRelative, protectedfile.ProtectedFileOptions{MaxBytes: maxBrowserAssociationsBytes})
	if err != nil {
		return BrowserAssociations{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var associations BrowserAssociations
	if err := decoder.Decode(&associations); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return BrowserAssociations{}, errors.New("invalid browser associations")
	}
	if associations.Format != BrowserAssociationsFormat || associations.Revision == 0 {
		return BrowserAssociations{}, errors.New("invalid browser associations")
	}
	seen := map[string]bool{}
	for _, entry := range associations.Entries {
		if !supportedBrowser(entry.Browser) || seen[entry.Browser] || !validBrowserExtensionID(entry.Browser, entry.ExtensionID) ||
			len(entry.Instances) == 0 || !slices.Contains(entry.Instances, entry.Identity) {
			return BrowserAssociations{}, errors.New("invalid browser associations")
		}
		for _, alias := range entry.Instances {
			if !validAlias(alias) {
				return BrowserAssociations{}, errors.New("invalid browser associations")
			}
		}
		seen[entry.Browser] = true
	}
	return associations, nil
}

// associationFor builds what the native host needs for one extension.
func (s Store) associationFor(extensionID string) (instanceselection.Association, error) {
	entry, revision, err := s.BrowserAssociationFor(extensionID)
	if err != nil {
		return instanceselection.Association{}, err
	}
	result := instanceselection.Association{Revision: revision, Identity: entry.Identity}
	for _, alias := range entry.Instances {
		instance, err := s.Get(alias)
		if err != nil {
			continue // an instance that was removed from the registry is no longer vouched for
		}
		result.Instances = append(result.Instances, instanceselection.Selection{Alias: alias, Revision: revision,
			RootObject: instance.RootObject, RootPath: instance.RootPath, HasPublication: instance.PublicationPath != ""})
	}
	if _, ok := result.Instance(entry.Identity); !ok {
		return instanceselection.Association{}, errors.New("the identity instance of this extension is no longer registered")
	}
	return result, nil
}
