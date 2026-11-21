/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package webhost serves an immutable Spark web publication. It contains no
// Brique identity, admission, Intention routing or UI build logic.
package webhost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultAddress                 = "127.0.0.1:0"
	maxFiles                       = 4096
	maxFileBytes                   = 32 << 20
	maxTotalBytes                  = 128 << 20
	publicationManifestName        = "brique-publication.json"
	publicationFormatV1            = "brique.spark-publication/1"
	publicationFormat              = "brique.spark-publication/2"
	bootstrapFormat                = "brique.spark-web-bootstrap/1"
	uiCatalogFormat                = "brique.spark-ui-catalog/1"
	uiCatalogName                  = "ui-catalog.json"
	publicWebObservedAddressHeader = "X-Brique-Observed-Address"
	publicWebClientAddressHeader   = "X-Brique-Client-Address"
	maxPublicationManifest         = 1 << 20
	maxBootstrapBytes              = 64 << 10
)

var hashedAsset = regexp.MustCompile(`(?i)(?:^|[._-])[0-9a-f]{8,64}(?:[._-]|$)`)
var publicationIDPattern = regexp.MustCompile(`^spub1_[A-Za-z0-9_-]{43}$`)
var publicationSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type publicationFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type publicationManifest struct {
	Format        string            `json:"format"`
	PublicationID string            `json:"publication_id"`
	Entrypoint    string            `json:"entrypoint"`
	Bootstrap     string            `json:"bootstrap"`
	Catalog       string            `json:"catalog,omitempty"`
	Files         []publicationFile `json:"files"`
}

type uiCatalogEntry struct {
	ContextPath string `json:"contextPath"`
	UIName      string `json:"uiName"`
	URL         string `json:"url"`
	Bootstrap   string `json:"bootstrap"`
}

type uiCatalog struct {
	Format  string           `json:"format"`
	Entries []uiCatalogEntry `json:"entries"`
}

type webBootstrap struct {
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

type Config struct {
	Address               string
	PublicationDirectory  string
	AllowedHosts          []string
	PublicOrigins         []string
	TrustedProxyAddresses []string
	PublicWebBackend      string
}

type PublicationInspection struct {
	Directory     string          `json:"directory"`
	PublicationID string          `json:"publication_id"`
	UIs           []PublicationUI `json:"-"`
}

type PublicationUI struct {
	ContextPath string
	UIName      string
	URL         string
}

type Server struct {
	server   *http.Server
	listener net.Listener
	url      string
}

type asset struct {
	contentType string
	data        []byte
	etag        string
	immutable   bool
}

func Start(config Config) (*Server, error) {
	if config.Address == "" {
		config.Address = DefaultAddress
	}
	if err := requireLoopbackAddress(config.Address); err != nil {
		return nil, err
	}
	assets, err := loadPublication(config.PublicationDirectory)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return nil, fmt.Errorf("listen web host: %w", err)
	}
	authorities, err := allowedAuthorities(listener.Addr().String(), config.AllowedHosts)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	trustedProxyAddresses, err := exactIPSet(config.TrustedProxyAddresses)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	publicOrigins, publicWebSocketOrigins, err := allowedPublicOrigins(listener.Addr().String(), config.PublicOrigins, authorities)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if config.PublicWebBackend == "" && len(config.PublicOrigins) != 0 {
		_ = listener.Close()
		return nil, errors.New("public web origins require a public web backend")
	}
	if config.PublicWebBackend != "" {
		publicWebSocketOrigins = append([]string{"ws://" + listener.Addr().String()}, publicWebSocketOrigins...)
	}
	handler := publicationHandler(assets, authorities, publicWebSocketOrigins)
	if config.PublicWebBackend != "" {
		publicOrigins["http://"+listener.Addr().String()] = true
		uiNames, namesErr := publicationUINameSet(assets)
		if namesErr != nil {
			_ = listener.Close()
			return nil, namesErr
		}
		handler, err = withPublicWebProxy(handler, config.PublicWebBackend, publicOrigins, publicWebSocketOrigins,
			authorities, uiNames, trustedProxyAddresses)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	result := &Server{server: httpServer, listener: listener, url: "http://" + listener.Addr().String() + "/"}
	go func() { _ = httpServer.Serve(listener) }()
	return result, nil
}

func withPublicWebProxy(static http.Handler, backendValue string, publicOrigins map[string]bool, publicWebSocketOrigins []string,
	authorities map[string]bool, uiNames map[string]bool, trustedProxyAddresses map[string]bool) (http.Handler, error) {
	backend, err := url.Parse(backendValue)
	host, _, splitErr := net.SplitHostPort(backend.Host)
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if err != nil || splitErr != nil || backend.Scheme != "ws" || ip == nil || !ip.IsLoopback() ||
		backend.User != nil || backend.RawQuery != "" || backend.Fragment != "" || backend.Path != "/__brique/public/" {
		return nil, errors.New("invalid public web backend")
	}
	target := &url.URL{Scheme: "http", Host: backend.Host}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		observedAddress, observedErr := observedClientIPAddress(request, trustedProxyAddresses)
		originalDirector(request)
		request.Host = backend.Host
		request.Header.Set("Origin", "http://"+backend.Host)
		request.Header.Del(publicWebObservedAddressHeader)
		if observedErr == nil {
			request.Header.Set(publicWebObservedAddressHeader, observedAddress)
		}
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, "public web unavailable", http.StatusServiceUnavailable)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/__brique/public/") {
			static.ServeHTTP(writer, request)
			return
		}
		setSecurityHeaders(writer.Header(), publicWebSocketOrigins)
		uiName := strings.TrimPrefix(request.URL.Path, "/__brique/public/")
		if request.Method != http.MethodGet || !authorities[normalizeAuthority(request.Host)] ||
			!publicOrigins[request.Header.Get("Origin")] || !uiNames[uiName] || strings.Contains(uiName, "/") {
			http.Error(writer, "public web socket refused", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(writer, request)
	}), nil
}

func observedClientIPAddress(request *http.Request, trustedProxyAddresses map[string]bool) (string, error) {
	direct, err := remoteIPAddress(request.RemoteAddr)
	if err != nil || !trustedProxyAddresses[direct] {
		return direct, err
	}
	values := request.Header.Values(publicWebClientAddressHeader)
	if len(values) != 1 {
		return "", errors.New("trusted proxy client address is missing or ambiguous")
	}
	client := net.ParseIP(strings.TrimSpace(values[0]))
	if client == nil {
		return "", errors.New("trusted proxy client address is not an IP")
	}
	return client.String(), nil
}

func exactIPSet(values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || result[ip.String()] {
			return nil, errors.New("invalid or duplicate trusted proxy address")
		}
		result[ip.String()] = true
	}
	return result, nil
}

func remoteIPAddress(remoteAddress string) (string, error) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("remote address is not an IP")
	}
	return ip.String(), nil
}

func publicationUINameSet(assets map[string]asset) (map[string]bool, error) {
	var manifest publicationManifest
	if err := decodeStrictJSON(assets["/"+publicationManifestName].data, &manifest); err != nil {
		return nil, errors.New("invalid publication manifest JSON")
	}
	uis, err := publicationUIs(assets, manifest)
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

// InspectPublication validates the complete immutable snapshot and returns its
// canonical directory and content-derived identity without opening a listener.
func InspectPublication(directory string) (PublicationInspection, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return PublicationInspection{}, errors.New("invalid publication directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return PublicationInspection{}, errors.New("publication directory is unavailable")
	}
	assets, err := loadPublication(canonical)
	if err != nil {
		return PublicationInspection{}, err
	}
	var manifest publicationManifest
	if err := json.Unmarshal(assets["/"+publicationManifestName].data, &manifest); err != nil {
		return PublicationInspection{}, errors.New("invalid publication manifest JSON")
	}
	uis, err := publicationUIs(assets, manifest)
	if err != nil {
		return PublicationInspection{}, err
	}
	return PublicationInspection{Directory: canonical, PublicationID: manifest.PublicationID, UIs: uis}, nil
}

func (server *Server) URL() string     { return server.url }
func (server *Server) Address() string { return server.listener.Addr().String() }

func (server *Server) Close(ctx context.Context) error {
	if server == nil || server.server == nil {
		return nil
	}
	err := server.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func loadPublication(directory string) (map[string]asset, error) {
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
	result := map[string]asset{}
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
			return fmt.Errorf("unsupported publication asset type: %s", urlPath)
		}
		data, err := os.ReadFile(filePath)
		if err != nil || int64(len(data)) != info.Size() {
			return errors.New("publication changed while loading")
		}
		digest := sha256.Sum256(data)
		result[urlPath] = asset{contentType: contentType, data: data,
			etag: `"sha256-` + hex.EncodeToString(digest[:]) + `"`, immutable: hashedAsset.MatchString(path.Base(urlPath))}
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
			item.immutable = true
			result[name] = item
		}
	}
	return result, nil
}

func verifyBootstrapAt(assets map[string]asset, bootstrapPath string) error {
	item, ok := assets[bootstrapPath]
	if !ok || len(item.data) == 0 || len(item.data) > maxBootstrapBytes {
		return errors.New("Spark web bootstrap is unavailable or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(item.data)))
	decoder.DisallowUnknownFields()
	var bootstrap webBootstrap
	if err := decoder.Decode(&bootstrap); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid Spark web bootstrap JSON")
	}
	if bootstrap.Format != bootstrapFormat || bootstrap.AdapterName != "react" ||
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

func verifyUICatalog(assets map[string]asset) error {
	var manifest publicationManifest
	if err := decodeStrictJSON(assets["/"+publicationManifestName].data, &manifest); err != nil {
		return errors.New("invalid publication manifest JSON")
	}
	_, err := publicationUIs(assets, manifest)
	return err
}

func publicationUIs(assets map[string]asset, manifest publicationManifest) ([]PublicationUI, error) {
	if manifest.Format == publicationFormatV1 {
		var bootstrap webBootstrap
		if err := decodeStrictJSON(assets["/bootstrap.json"].data, &bootstrap); err != nil {
			return nil, errors.New("invalid legacy Spark UI bootstrap")
		}
		return []PublicationUI{{ContextPath: bootstrap.ContextPath, UIName: bootstrap.UIName, URL: "/"}}, nil
	}
	item, ok := assets["/"+uiCatalogName]
	if !ok || len(item.data) == 0 || len(item.data) > maxBootstrapBytes {
		return nil, errors.New("Spark UI catalog is unavailable or too large")
	}
	var catalog uiCatalog
	if err := decodeStrictJSON(item.data, &catalog); err != nil || catalog.Format != uiCatalogFormat || len(catalog.Entries) == 0 {
		return nil, errors.New("invalid Spark UI catalog")
	}
	seenCoordinates := map[string]bool{}
	seenURLs := map[string]bool{}
	result := make([]PublicationUI, 0, len(catalog.Entries))
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
		var bootstrap webBootstrap
		if err := decodeStrictJSON(assets[entry.Bootstrap].data, &bootstrap); err != nil ||
			bootstrap.ContextPath != entry.ContextPath || bootstrap.UIName != entry.UIName {
			return nil, errors.New("Spark UI catalog does not match bootstrap")
		}
		seenCoordinates[coordinate], seenURLs[entry.URL] = true, true
		result = append(result, PublicationUI{ContextPath: entry.ContextPath, UIName: entry.UIName, URL: entry.URL})
	}
	return result, nil
}

func validUIRoute(value string) bool {
	if !strings.HasPrefix(value, "/ui/ui1_") || !strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(value, "/ui/"), "/")
	return len(token) == 47 && strings.HasPrefix(token, "ui1_") && publicationIDPattern.MatchString("spub1_"+strings.TrimPrefix(token, "ui1_"))
}

func decodeStrictJSON(data []byte, value any) error {
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

func verifyPublicationManifest(assets map[string]asset) error {
	manifestAsset, ok := assets["/"+publicationManifestName]
	if !ok || len(manifestAsset.data) == 0 || len(manifestAsset.data) > maxPublicationManifest {
		return errors.New("publication manifest is unavailable or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestAsset.data)))
	decoder.DisallowUnknownFields()
	var manifest publicationManifest
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid publication manifest JSON")
	}
	validFormat := (manifest.Format == publicationFormatV1 && manifest.Catalog == "") ||
		(manifest.Format == publicationFormat && manifest.Catalog == uiCatalogName)
	if !validFormat || !publicationIDPattern.MatchString(manifest.PublicationID) ||
		manifest.Entrypoint != "index.html" || manifest.Bootstrap != "bootstrap.json" || len(manifest.Files) == 0 {
		return errors.New("invalid publication manifest identity")
	}
	actual := make([]publicationFile, 0, len(assets)-1)
	for urlPath, item := range assets {
		name := strings.TrimPrefix(urlPath, "/")
		if name == publicationManifestName {
			continue
		}
		digest := sha256.Sum256(item.data)
		actual = append(actual, publicationFile{Path: name, Size: int64(len(item.data)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Path < actual[j].Path })
	if len(actual) != len(manifest.Files) {
		return errors.New("publication file inventory does not match manifest")
	}
	previous := ""
	for index, file := range manifest.Files {
		if file.Path == "" || file.Path <= previous || file.Path == publicationManifestName || !publicationSHA256Pattern.MatchString(file.SHA256) || file != actual[index] {
			return errors.New("publication file integrity check failed")
		}
		previous = file.Path
	}
	if manifest.PublicationID != publicationManifestID(manifest) {
		return errors.New("publication identifier does not match manifest content")
	}
	return nil
}

func publicationManifestID(manifest publicationManifest) string {
	hash := sha256.New()
	add := func(value string) { _, _ = fmt.Fprintf(hash, "%d:%s\n", len([]byte(value)), value) }
	add(manifest.Format)
	add(manifest.Entrypoint)
	add(manifest.Bootstrap)
	if manifest.Format == publicationFormat {
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

// staticWriteIdleTimeout is how long one chunk of a static response may stay unread by the
// client. The deadline slides forward with progress, so a slow client that keeps reading is
// served while one that stops holds neither a goroutine nor a descriptor. A server-wide
// WriteTimeout would also cut the proxied WebSocket, hence the per-response control.
var staticWriteIdleTimeout = 15 * time.Second

const staticWriteChunkBytes = 64 << 10

func writeWithIdleDeadline(writer http.ResponseWriter, data []byte) {
	controller := http.NewResponseController(writer)
	for len(data) > 0 {
		chunk := data
		if len(chunk) > staticWriteChunkBytes {
			chunk = chunk[:staticWriteChunkBytes]
		}
		// A writer without deadline support (a test recorder) is written through unbounded.
		_ = controller.SetWriteDeadline(time.Now().Add(staticWriteIdleTimeout))
		written, err := writer.Write(chunk)
		if err != nil {
			return
		}
		data = data[written:]
	}
	_ = controller.SetWriteDeadline(time.Time{})
}

func publicationHandler(assets map[string]asset, authorities map[string]bool, publicWebSocketOrigins []string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer.Header(), publicWebSocketOrigins)
		if !authorities[normalizeAuthority(request.Host)] {
			http.Error(writer, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path == "/__brique/health" {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.Header().Set("Cache-Control", "no-store")
			if request.Method == http.MethodGet {
				_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ready"})
			}
			return
		}
		cleaned := path.Clean(request.URL.Path)
		escapedPath := strings.ToLower(request.URL.EscapedPath())
		validTrailingSlash := request.URL.Path != "/" && strings.HasSuffix(request.URL.Path, "/") && cleaned+"/" == request.URL.Path
		if (cleaned != request.URL.Path && !validTrailingSlash) || strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
			http.NotFound(writer, request)
			return
		}
		if cleaned == "/" {
			cleaned = "/index.html"
		} else if validTrailingSlash {
			cleaned += "/index.html"
		}
		if cleaned == "/"+publicationManifestName || cleaned == "/"+uiCatalogName {
			http.NotFound(writer, request)
			return
		}
		item, ok := assets[cleaned]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", item.contentType)
		writer.Header().Set("ETag", item.etag)
		if strings.HasSuffix(cleaned, "/index.html") || strings.HasSuffix(cleaned, "/bootstrap.json") || cleaned == "/"+uiCatalogName {
			writer.Header().Set("Cache-Control", "no-store")
		} else if item.immutable {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		if request.Header.Get("If-None-Match") == item.etag {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("Content-Length", fmt.Sprint(len(item.data)))
		if request.Method == http.MethodGet {
			writeWithIdleDeadline(writer, item.data)
		}
	})
}

func setSecurityHeaders(header http.Header, publicWebSocketOrigins []string) {
	connect := "'self'"
	for _, origin := range publicWebSocketOrigins {
		connect += " " + origin
	}
	header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src "+connect+"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func allowedPublicOrigins(bound string, configured []string, authorities map[string]bool) (map[string]bool, []string, error) {
	origins := make(map[string]bool, len(configured)+1)
	webSockets := make([]string, 0, len(configured))
	for _, value := range configured {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || value != "https://"+parsed.Host {
			return nil, nil, errors.New("invalid public web origin")
		}
		authority := normalizeAuthority(parsed.Host)
		if authority == "" || !authorities[authority] || origins[value] {
			return nil, nil, errors.New("public web origin does not match an allowed host")
		}
		origins[value] = true
		webSockets = append(webSockets, "wss://"+parsed.Host)
	}
	sort.Strings(webSockets)
	return origins, webSockets, nil
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

// ValidateLoopbackAddress reports whether address is an explicit loopback host:port.
func ValidateLoopbackAddress(address string) error { return requireLoopbackAddress(address) }

// ValidateServerProfile applies, without binding anything, the exact checks Start
// applies to a server profile: a fixed loopback address, exact public authorities,
// exact HTTPS origins matching those authorities and exact reverse-proxy IPs.
// The launcher uses it so that an incoherent profile is refused when recorded,
// not only when the web host starts.
func ValidateServerProfile(address string, allowedHosts, publicOrigins, trustedProxies []string) error {
	if err := requireLoopbackAddress(address); err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(address)
	if portNumber, err := net.LookupPort("tcp", port); err != nil || portNumber == 0 {
		return errors.New("server profile requires a fixed web host port")
	}
	if len(allowedHosts) == 0 || len(publicOrigins) == 0 || len(trustedProxies) == 0 {
		return errors.New("server profile requires an allowed host, a public origin and a trusted proxy")
	}
	authorities, err := allowedAuthorities(address, allowedHosts)
	if err != nil {
		return err
	}
	if _, err := exactIPSet(trustedProxies); err != nil {
		return err
	}
	_, _, err = allowedPublicOrigins(address, publicOrigins, authorities)
	return err
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("web host address must include host and port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("web host currently requires an explicit loopback address")
	}
	return nil
}

func allowedAuthorities(bound string, configured []string) (map[string]bool, error) {
	result := map[string]bool{normalizeAuthority(bound): true}
	for _, value := range configured {
		normalized := normalizeAuthority(value)
		if normalized == "" || strings.ContainsAny(value, "/\\@") {
			return nil, errors.New("invalid allowed web host authority")
		}
		result[normalized] = true
	}
	return result, nil
}

func normalizeAuthority(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		if strings.ContainsAny(value, "[]:") || value == "" {
			return ""
		}
		host = value
		port = ""
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}
