/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package webhost serves an immutable Spark web publication. It contains no
// Brique identity, admission, Intention routing or UI build logic.
package publication

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxFiles               = 4096
	maxFileBytes           = 32 << 20
	maxTotalBytes          = 128 << 20
	ManifestName           = "brique-publication.json"
	FormatV1               = "brique.spark-publication/1"
	Format                 = "brique.spark-publication/2"
	BootstrapFormat        = "brique.spark-web-bootstrap/1"
	UICatalogFormat        = "brique.spark-ui-catalog/1"
	UICatalogName          = "ui-catalog.json"
	maxPublicationManifest = 1 << 20
	maxBootstrapBytes      = 64 << 10
)

var hashedAsset = regexp.MustCompile(`(?i)(?:^|[._-])[0-9a-f]{8,64}(?:[._-]|$)`)

var IDPattern = regexp.MustCompile(`^spub1_[A-Za-z0-9_-]{43}$`)

var publicationSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format        string `json:"format"`
	PublicationID string `json:"publication_id"`
	Entrypoint    string `json:"entrypoint"`
	Bootstrap     string `json:"bootstrap"`
	Catalog       string `json:"catalog,omitempty"`
	Files         []File `json:"files"`
}

type UICatalogEntry struct {
	ContextPath string `json:"contextPath"`
	UIName      string `json:"uiName"`
	URL         string `json:"url"`
	Bootstrap   string `json:"bootstrap"`
}

type UICatalog struct {
	Format  string           `json:"format"`
	Entries []UICatalogEntry `json:"entries"`
}

type Bootstrap struct {
	Format            string   `json:"format"`
	ContextPath       string   `json:"contextPath"`
	ChildContextPaths []string `json:"childContextPaths"`
	UIName            string   `json:"uiName"`
	UIAddressPrefix   string   `json:"uiAddressPrefix"`
	RootContextID     string   `json:"rootContextId"`
	RootViewName      string   `json:"rootViewName"`
	RootViewPath      string   `json:"rootViewPath"`
	AdapterName       string   `json:"adapterName"`
}

type Inspection struct {
	Directory     string `json:"directory"`
	PublicationID string `json:"publication_id"`
	UIs           []UI   `json:"-"`
}

type UI struct {
	ContextPath string
	UIName      string
	URL         string
}

type Asset struct {
	ContentType string
	Data        []byte
	ETag        string
	Immutable   bool
}

func UINameSet(assets map[string]Asset) (map[string]bool, error) {
	var manifest Manifest
	if err := DecodeStrictJSON(assets["/"+ManifestName].Data, &manifest); err != nil {
		return nil, errors.New("invalid publication manifest JSON")
	}
	uis, err := UIs(assets, manifest)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(uis))
	for _, ui := range uis {
		if ui.UIName == "" || result[ui.UIName] {
			return nil, errors.New("publication UI names are invalid")
		}
		result[ui.UIName] = true
	}
	return result, nil
}

// Inspect validates the complete immutable snapshot and returns its
// canonical directory and content-derived identity without opening a listener.
func Inspect(directory string) (Inspection, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Inspection{}, errors.New("invalid publication directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Inspection{}, errors.New("publication directory is unavailable")
	}
	assets, err := Load(canonical)
	if err != nil {
		return Inspection{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(assets["/"+ManifestName].Data, &manifest); err != nil {
		return Inspection{}, errors.New("invalid publication manifest JSON")
	}
	uis, err := UIs(assets, manifest)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{Directory: canonical, PublicationID: manifest.PublicationID, UIs: uis}, nil
}

func Load(directory string) (map[string]Asset, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("publication directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("invalid publication directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errors.New("publication directory is unavailable")
	}
	rootInfo, err := os.Stat(canonical)
	if err != nil || !rootInfo.IsDir() {
		return nil, errors.New("publication path is not a directory")
	}
	result := map[string]Asset{}
	var total int64
	err = filepath.WalkDir(canonical, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("publication is unreadable")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("publication contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || len(result) >= maxFiles {
			return errors.New("publication file inventory is invalid or too large")
		}
		info, err := entry.Info()
		if err != nil || info.Size() < 0 || info.Size() > maxFileBytes || total+info.Size() > maxTotalBytes {
			return errors.New("publication byte inventory is too large")
		}
		relative, err := filepath.Rel(canonical, filePath)
		if err != nil || relative == "." {
			return errors.New("invalid publication path")
		}
		urlPath := "/" + filepath.ToSlash(relative)
		contentType, ok := contentTypeFor(urlPath)
		if !ok {
			return fmt.Errorf("unsupported publication Asset type: %s", urlPath)
		}
		data, err := os.ReadFile(filePath)
		if err != nil || int64(len(data)) != info.Size() {
			return errors.New("publication changed while loading")
		}
		digest := sha256.Sum256(data)
		result[urlPath] = Asset{ContentType: contentType, Data: data,
			ETag: `"sha256-` + hex.EncodeToString(digest[:]) + `"`, Immutable: hashedAsset.MatchString(path.Base(urlPath))}
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := result["/index.html"]; !ok {
		return nil, errors.New("publication is missing index.html")
	}
	if err := verifyPublicationManifest(result); err != nil {
		return nil, err
	}
	if err := verifyBootstrapAt(result, "/bootstrap.json"); err != nil {
		return nil, err
	}
	if err := verifyUICatalog(result); err != nil {
		return nil, err
	}
	for name, item := range result {
		if strings.HasPrefix(name, "/assets/") {
			item.Immutable = true
			result[name] = item
		}
	}
	return result, nil
}

func verifyBootstrapAt(assets map[string]Asset, bootstrapPath string) error {
	item, ok := assets[bootstrapPath]
	if !ok || len(item.Data) == 0 || len(item.Data) > maxBootstrapBytes {
		return errors.New("Spark web bootstrap is unavailable or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(item.Data)))
	decoder.DisallowUnknownFields()
	var bootstrap Bootstrap
	if err := decoder.Decode(&bootstrap); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid Spark web bootstrap JSON")
	}
	if bootstrap.Format != BootstrapFormat || bootstrap.AdapterName != "react" ||
		bootstrap.ContextPath != bootstrap.RootContextID || !logicalContextPath(bootstrap.RootContextID) ||
		strings.TrimSpace(bootstrap.UIName) == "" || bootstrap.UIAddressPrefix != "@ui_"+bootstrap.UIName ||
		strings.TrimSpace(bootstrap.RootViewName) == "" || !publicationAssetPath(bootstrap.RootViewPath) {
		return errors.New("invalid Spark web bootstrap identity")
	}
	if _, ok := assets[bootstrap.RootViewPath]; !ok {
		return errors.New("Spark web bootstrap root view is missing")
	}
	prefix := strings.TrimSuffix(bootstrap.RootContextID, "/") + "/"
	previous := ""
	for _, child := range bootstrap.ChildContextPaths {
		if !logicalContextPath(child) || !strings.HasPrefix(child, prefix) || child <= previous {
			return errors.New("invalid Spark web bootstrap child inventory")
		}
		previous = child
	}
	return nil
}

func verifyUICatalog(assets map[string]Asset) error {
	var manifest Manifest
	if err := DecodeStrictJSON(assets["/"+ManifestName].Data, &manifest); err != nil {
		return errors.New("invalid publication manifest JSON")
	}
	_, err := UIs(assets, manifest)
	return err
}

func UIs(assets map[string]Asset, manifest Manifest) ([]UI, error) {
	if manifest.Format == FormatV1 {
		var bootstrap Bootstrap
		if err := DecodeStrictJSON(assets["/bootstrap.json"].Data, &bootstrap); err != nil {
			return nil, errors.New("invalid legacy Spark UI bootstrap")
		}
		return []UI{{ContextPath: bootstrap.ContextPath, UIName: bootstrap.UIName, URL: "/"}}, nil
	}
	item, ok := assets["/"+UICatalogName]
	if !ok || len(item.Data) == 0 || len(item.Data) > maxBootstrapBytes {
		return nil, errors.New("Spark UI catalog is unavailable or too large")
	}
	var catalog UICatalog
	if err := DecodeStrictJSON(item.Data, &catalog); err != nil || catalog.Format != UICatalogFormat || len(catalog.Entries) == 0 {
		return nil, errors.New("invalid Spark UI catalog")
	}
	seenCoordinates := map[string]bool{}
	seenURLs := map[string]bool{}
	result := make([]UI, 0, len(catalog.Entries))
	for index, entry := range catalog.Entries {
		coordinate := entry.ContextPath + "\x00" + entry.UIName
		if !logicalContextPath(entry.ContextPath) || strings.TrimSpace(entry.UIName) == "" || entry.UIName != strings.TrimSpace(entry.UIName) ||
			strings.ContainsAny(entry.UIName, "\x00\r\n") || seenCoordinates[coordinate] || seenURLs[entry.URL] {
			return nil, errors.New("invalid Spark UI catalog entry")
		}
		indexPath := ""
		if index == 0 {
			if entry.URL != "/" || entry.Bootstrap != "/bootstrap.json" {
				return nil, errors.New("invalid Spark UI root catalog entry")
			}
			indexPath = "/index.html"
		} else {
			if !validUIRoute(entry.URL) || entry.Bootstrap != entry.URL+"bootstrap.json" {
				return nil, errors.New("invalid Spark UI catalog route")
			}
			indexPath = entry.URL + "index.html"
		}
		if _, ok := assets[indexPath]; !ok {
			return nil, errors.New("Spark UI catalog entrypoint is missing")
		}
		if err := verifyBootstrapAt(assets, entry.Bootstrap); err != nil {
			return nil, err
		}
		var bootstrap Bootstrap
		if err := DecodeStrictJSON(assets[entry.Bootstrap].Data, &bootstrap); err != nil ||
			bootstrap.ContextPath != entry.ContextPath || bootstrap.UIName != entry.UIName {
			return nil, errors.New("Spark UI catalog does not match bootstrap")
		}
		seenCoordinates[coordinate], seenURLs[entry.URL] = true, true
		result = append(result, UI{ContextPath: entry.ContextPath, UIName: entry.UIName, URL: entry.URL})
	}
	return result, nil
}

func validUIRoute(value string) bool {
	if !strings.HasPrefix(value, "/ui/ui1_") || !strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(value, "/ui/"), "/")
	return len(token) == 47 && strings.HasPrefix(token, "ui1_") && IDPattern.MatchString("spub1_"+strings.TrimPrefix(token, "ui1_"))
}

func DecodeStrictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func logicalContextPath(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && !strings.Contains(value, "\\") &&
		!strings.ContainsAny(value, "\x00\r\n") && path.Clean(value) == value
}

func publicationAssetPath(value string) bool {
	return strings.HasPrefix(value, "/assets/") && !strings.Contains(value, "\\") &&
		!strings.ContainsAny(value, "\x00\r\n") && path.Clean(value) == value
}

func verifyPublicationManifest(assets map[string]Asset) error {
	manifestAsset, ok := assets["/"+ManifestName]
	if !ok || len(manifestAsset.Data) == 0 || len(manifestAsset.Data) > maxPublicationManifest {
		return errors.New("publication manifest is unavailable or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestAsset.Data)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid publication manifest JSON")
	}
	validFormat := (manifest.Format == FormatV1 && manifest.Catalog == "") ||
		(manifest.Format == Format && manifest.Catalog == UICatalogName)
	if !validFormat || !IDPattern.MatchString(manifest.PublicationID) ||
		manifest.Entrypoint != "index.html" || manifest.Bootstrap != "bootstrap.json" || len(manifest.Files) == 0 {
		return errors.New("invalid publication manifest identity")
	}
	actual := make([]File, 0, len(assets)-1)
	for urlPath, item := range assets {
		name := strings.TrimPrefix(urlPath, "/")
		if name == ManifestName {
			continue
		}
		digest := sha256.Sum256(item.Data)
		actual = append(actual, File{Path: name, Size: int64(len(item.Data)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Path < actual[j].Path })
	if len(actual) != len(manifest.Files) {
		return errors.New("publication file inventory does not match manifest")
	}
	previous := ""
	for index, file := range manifest.Files {
		if file.Path == "" || file.Path <= previous || file.Path == ManifestName || !publicationSHA256Pattern.MatchString(file.SHA256) || file != actual[index] {
			return errors.New("publication file integrity check failed")
		}
		previous = file.Path
	}
	if manifest.PublicationID != ManifestID(manifest) {
		return errors.New("publication identifier does not match manifest content")
	}
	return nil
}

func ManifestID(manifest Manifest) string {
	hash := sha256.New()
	add := func(value string) { _, _ = fmt.Fprintf(hash, "%d:%s\n", len([]byte(value)), value) }
	add(manifest.Format)
	add(manifest.Entrypoint)
	add(manifest.Bootstrap)
	if manifest.Format == Format {
		add(manifest.Catalog)
	}
	add(fmt.Sprint(len(manifest.Files)))
	for _, file := range manifest.Files {
		add(file.Path)
		add(fmt.Sprint(file.Size))
		add(file.SHA256)
	}
	return "spub1_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func contentTypeFor(name string) (string, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8", true
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8", true
	case ".css":
		return "text/css; charset=utf-8", true
	case ".json", ".map":
		return "application/json; charset=utf-8", true
	case ".svg":
		return "image/svg+xml", true
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".ico":
		return "image/x-icon", true
	case ".woff2":
		return "font/woff2", true
	case ".wasm":
		return "application/wasm", true
	default:
		if value := mime.TypeByExtension(path.Ext(name)); value != "" && strings.HasPrefix(value, "text/") {
			return value, true
		}
		return "", false
	}
}
