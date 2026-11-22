/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"errors"

	"brique_engine/contracts/instanceselection"
)

// InstanceResolver answers the browser native host from the launcher registry and
// the launcher control endpoint. It performs exactly the strict reads of Store and
// CallLauncherControl; it does not interpret a partial registry.
type InstanceResolver struct {
	store                   Store
	directory               string
	controlRuntimeDirectory string
}

// NewInstanceResolver uses the default data directory when directory is empty.
func NewInstanceResolver(directory, controlRuntimeDirectory string) (*InstanceResolver, error) {
	if directory == "" {
		var err error
		directory, err = DefaultDataDirectory()
		if err != nil {
			return nil, err
		}
	}
	return &InstanceResolver{store: Store{Directory: directory}, directory: directory,
		controlRuntimeDirectory: controlRuntimeDirectory}, nil
}

// Association is what the owner chose for the extension that started a native host.
func (resolver *InstanceResolver) Association(extensionID string) (instanceselection.Association, error) {
	return resolver.store.associationFor(extensionID)
}

func (resolver *InstanceResolver) WebStatus(alias string) (instanceselection.WebStatus, error) {
	response, err := CallLauncherControl(resolver.directory, resolver.controlRuntimeDirectory,
		LauncherControlRequest{Operation: "status", Alias: alias})
	if err != nil || !response.OK || response.Status == nil || response.Status.Alias != alias {
		return instanceselection.WebStatus{}, errors.New("launcher status unavailable")
	}
	return instanceselection.WebStatus{Alias: response.Status.Alias, Ready: response.Status.State == StateReady,
		WebPID: response.Status.WebPID, WebURL: response.Status.WebURL}, nil
}
