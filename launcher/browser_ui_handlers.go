/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"errors"
	"net/http"
	"slices"
)

type webUIBrowser struct {
	BrowserDescriptor
	Installed bool `json:"installed"`
	// Folder is where the extension of this browser is loaded from; ExtensionID is the identifier it has there.
	Folder      string   `json:"folder"`
	ExtensionID string   `json:"extension_id"`
	Associated  bool     `json:"associated"`
	Instances   []string `json:"instances"`
	Identity    string   `json:"identity"`
}

type webUIBrowserSection struct {
	// Instances are the instances that can be associated: those that run from a runtime package.
	Instances []string       `json:"instances"`
	Browsers  []webUIBrowser `json:"browsers"`
}

func (ui *WebUI) browserSection(_ *http.Request) (any, int, error) {
	section := webUIBrowserSection{Instances: []string{}, Browsers: []webUIBrowser{}}
	if registry, err := ui.options.Store.List(); err == nil {
		for _, instance := range registry.Instances {
			if instance.RuntimePath != "" {
				section.Instances = append(section.Instances, instance.Alias)
			}
		}
	}
	associations, _ := ui.options.Store.BrowserAssociations()
	for _, descriptor := range supportedBrowsers {
		_, installed := browserApplication(descriptor.Name)
		item := webUIBrowser{BrowserDescriptor: descriptor, Installed: installed, Instances: []string{},
			Folder: extensionFolder(ui.options.DataDirectory, descriptor.Name), ExtensionID: browserExtensionID(ui.options.DataDirectory, descriptor.Name)}
		if entry, ok := associations.forBrowser(descriptor.Name); ok {
			item.Associated, item.Instances, item.Identity = true, entry.Instances, entry.Identity
		}
		section.Browsers = append(section.Browsers, item)
	}
	return section, 0, nil
}

type webUIAssociateRequest struct {
	Browser   string   `json:"browser"`
	Instances []string `json:"instances"`
	Identity  string   `json:"identity"`
}

// associate chooses the instances an extension vouches for and the one that carries the identity. It
// prepares the extension and registers the native host; the person only has to load the extension.
func (ui *WebUI) associate(r *http.Request) (any, int, error) {
	var request webUIAssociateRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	entry, err := ui.options.Store.SetBrowserAssociation(r.Context(), request.Browser, request.Instances, request.Identity)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	return entry, 0, nil
}

func (ui *WebUI) dissociate(r *http.Request) (any, int, error) {
	var request struct {
		Browser string `json:"browser"`
	}
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if err := ui.options.Store.ClearBrowserAssociation(r.Context(), request.Browser); err != nil {
		return nil, http.StatusBadRequest, err
	}
	return map[string]bool{"ok": true}, 0, nil
}

type webUIOpenRequest struct {
	Browser string `json:"browser"`
	// Target is "extensions" (the browser's own extensions page) or "instance" (the web page of one of the
	// instances associated with that browser, named by Alias). A page never supplies an address.
	Target string `json:"target"`
	Alias  string `json:"alias"`
}

func (ui *WebUI) openBrowser(r *http.Request) (any, int, error) {
	var request webUIOpenRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if _, ok := browserDescriptor(request.Browser); !ok {
		return nil, http.StatusBadRequest, errors.New("unsupported browser")
	}
	var address string
	switch request.Target {
	case "extensions":
		address = extensionsPage(request.Browser)
	case "instance":
		associations, _ := ui.options.Store.BrowserAssociations()
		entry, ok := associations.forBrowser(request.Browser)
		if !ok || !slices.Contains(entry.Instances, request.Alias) {
			return nil, http.StatusConflict, errors.New("this instance is not associated with the extension of this browser")
		}
		response, err := ui.call(LauncherControlRequest{Operation: "status", Alias: request.Alias})
		if err != nil || response.Status == nil || response.Status.State != StateReady || response.Status.WebURL == "" {
			return nil, http.StatusConflict, errors.New("the instance is not started or has no published web page")
		}
		address = response.Status.WebURL
	default:
		return nil, http.StatusBadRequest, errors.New("unknown target")
	}
	open := ui.options.OpenBrowser
	if open == nil {
		open = OpenInBrowser
	}
	if err := open(request.Browser, address); err != nil {
		return nil, http.StatusBadGateway, err
	}
	return map[string]string{"opened": address}, 0, nil
}
