/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package reflexive

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

const (
	uiContextPathParam = "contextPath"
	uiNameParam        = "uiName"
	uiURLField         = "url"
	maxUIContextBytes  = 1 << 20
)

type webUIEntry struct {
	ContextPath string `json:"contextPath"`
	UIName      string `json:"uiName"`
	URL         string `json:"url"`
}

type webUIContextDescriptor struct {
	Brique struct {
		UIConfig *struct {
			RootView string `json:"root_view"`
			Adapter  string `json:"adapter"`
			Name     string `json:"name"`
		} `json:"ui_config"`
	} `json:"brique"`
}

func (l *ReflexiveLoop) capUIList(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	entries, err := l.activeWebUIs()
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "ui_catalog_unavailable"}, "UI catalog unavailable"))
		return
	}
	if scope := l.externalReadScope(in); scope != nil {
		filtered := entries[:0]
		for _, entry := range entries {
			if scope.CanRead(entry.ContextPath, circulation.ValueContext, "") {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	l.emitResponseOK(in, map[string]any{circulation.KeyResult: entries})
}

func (l *ReflexiveLoop) capUIOpen(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention
	contextPath, uiName, err := parseUIOpenParams(in)
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: "invalid_ui_coordinate"}, "invalid UI coordinate"))
		return
	}
	entries, err := l.activeWebUIs()
	if err != nil {
		l.emitResponseError(errorResp(in, circulation.ValueCodeUnavailable,
			map[string]any{circulation.KeyReason: "ui_catalog_unavailable"}, "UI catalog unavailable"))
		return
	}
	for _, entry := range entries {
		if entry.ContextPath == contextPath && entry.UIName == uiName {
			l.emitResponseOK(in, map[string]any{
				uiContextPathParam: entry.ContextPath,
				uiNameParam:        entry.UIName,
				uiURLField:         entry.URL,
			})
			return
		}
	}
	l.emitResponseError(errorResp(in, circulation.ValueCodeNotFound,
		map[string]any{circulation.KeyReason: "ui_not_found"}, "UI not found"))
}

func (l *ReflexiveLoop) activeWebUIs() ([]webUIEntry, error) {
	if l == nil || l.frame == nil || l.frame.CtxId != shared.RootContextID || l.frame.ContextDir == "" {
		return nil, fmt.Errorf("UI catalog is available only from the root context")
	}
	provider, ok := l.frame.CtxCommReg.(junction.ContextInventoryProvider)
	if !ok {
		return nil, fmt.Errorf("active context inventory is unavailable")
	}
	publishedProvider, ok := l.frame.CtxCommReg.(junction.PublishedUIProvider)
	if !ok {
		return nil, fmt.Errorf("published UI inventory is unavailable")
	}
	_, published, installed := publishedProvider.PublishedUIInventory()
	if !installed {
		return nil, fmt.Errorf("no web publication is associated with this instance")
	}
	root, err := filepath.EvalSymlinks(l.frame.ContextDir)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, id := range provider.ActiveContextIDs() {
		active[string(id)] = true
	}
	result := make([]webUIEntry, 0, len(published))
	for _, publishedUI := range published {
		contextID := string(publishedUI.ContextPath)
		if !active[contextID] {
			continue
		}
		relative, valid := relativeRootContext(contextID)
		if !valid {
			continue
		}
		directory := root
		if relative != "" {
			directory = filepath.Join(root, filepath.FromSlash(relative))
		}
		entry, exists, err := readWebUIEntry(root, directory, contextID)
		if err != nil {
			return nil, err
		}
		if exists && entry.UIName == publishedUI.UIName && entry.URL == publishedUI.URL {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ContextPath == result[j].ContextPath {
			return result[i].UIName < result[j].UIName
		}
		return result[i].ContextPath < result[j].ContextPath
	})
	return result, nil
}

func readWebUIEntry(root, directory, contextID string) (webUIEntry, bool, error) {
	descriptorPath, err := filepath.EvalSymlinks(filepath.Join(directory, ContextDescriptorFilename))
	if err != nil {
		return webUIEntry{}, false, err
	}
	relative, err := filepath.Rel(root, descriptorPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return webUIEntry{}, false, fmt.Errorf("context descriptor escapes the instance root")
	}
	file, err := os.Open(descriptorPath)
	if err != nil {
		return webUIEntry{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 0 || info.Size() > maxUIContextBytes {
		return webUIEntry{}, false, fmt.Errorf("context descriptor is too large")
	}
	var descriptor webUIContextDescriptor
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&descriptor); err != nil {
		return webUIEntry{}, false, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return webUIEntry{}, false, fmt.Errorf("context descriptor has trailing JSON")
	}
	config := descriptor.Brique.UIConfig
	if config == nil || strings.TrimSpace(config.RootView) == "" || config.RootView == "BriqueEditorRoot" || config.Adapter != "react" {
		return webUIEntry{}, false, nil
	}
	uiName := strings.TrimSpace(config.Name)
	if uiName == "" {
		uiName = "main"
	}
	if strings.ContainsAny(uiName, "\x00\r\n") {
		return webUIEntry{}, false, fmt.Errorf("invalid UI name")
	}
	return webUIEntry{ContextPath: contextID, UIName: uiName, URL: webUIURL(contextID, uiName)}, true, nil
}

func relativeRootContext(contextID string) (string, bool) {
	if contextID == shared.RootContextID {
		return "", true
	}
	prefix := shared.RootContextID + "/"
	if !strings.HasPrefix(contextID, prefix) || strings.Contains(contextID, "\\") || strings.ContainsAny(contextID, "\x00\r\n") {
		return "", false
	}
	relative := strings.TrimPrefix(contextID, prefix)
	for _, segment := range strings.Split(relative, "/") {
		if _, valid := validateSimpleID(segment); !valid {
			return "", false
		}
	}
	return relative, true
}

func webUIURL(contextPath, uiName string) string {
	if contextPath == shared.RootContextID {
		return "/"
	}
	digest := sha256.Sum256([]byte(contextPath + "\x00" + uiName))
	return "/ui/ui1_" + base64.RawURLEncoding.EncodeToString(digest[:]) + "/"
}

func parseUIOpenParams(in circulation.Intention) (string, string, error) {
	contextPath, _ := in.Params[uiContextPathParam].(string)
	uiName, _ := in.Params[uiNameParam].(string)
	contextPath, uiName = strings.TrimSpace(contextPath), strings.TrimSpace(uiName)
	if _, valid := relativeRootContext(contextPath); !valid || uiName == "" || strings.ContainsAny(uiName, "\x00\r\n") {
		return "", "", fmt.Errorf("invalid UI coordinate")
	}
	return contextPath, uiName, nil
}
