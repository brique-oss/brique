/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package webhost serves an immutable Spark web publication. It contains no
// Brique identity, admission, Intention routing or UI build logic.
package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"brique_engine/contracts/publication"
	"brique_engine/contracts/webprofile"
)

const (
	DefaultAddress                 = "127.0.0.1:0"
	publicWebObservedAddressHeader = "X-Brique-Observed-Address"
	publicWebClientAddressHeader   = "X-Brique-Client-Address"
)

type Config struct {
	Address               string
	PublicationDirectory  string
	AllowedHosts          []string
	PublicOrigins         []string
	TrustedProxyAddresses []string
	PublicWebBackend      string
}

type Server struct {
	server   *http.Server
	listener net.Listener
	url      string
}

func Start(config Config) (*Server, error) {
	if config.Address == "" {
		config.Address = DefaultAddress
	}
	if err := webprofile.RequireLoopbackAddress(config.Address); err != nil {
		return nil, err
	}
	assets, err := publication.Load(config.PublicationDirectory)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return nil, fmt.Errorf("listen web host: %w", err)
	}
	authorities, err := webprofile.AllowedAuthorities(listener.Addr().String(), config.AllowedHosts)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	trustedProxyAddresses, err := webprofile.ExactIPSet(config.TrustedProxyAddresses)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	publicOrigins, publicWebSocketOrigins, err := webprofile.AllowedPublicOrigins(listener.Addr().String(), config.PublicOrigins, authorities)
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
		uiNames, namesErr := publication.UINameSet(assets)
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
		if request.Method != http.MethodGet || !authorities[webprofile.NormalizeAuthority(request.Host)] ||
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

func (server *Server) URL() string { return server.url }

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

func publicationHandler(assets map[string]publication.Asset, authorities map[string]bool, publicWebSocketOrigins []string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer.Header(), publicWebSocketOrigins)
		if !authorities[webprofile.NormalizeAuthority(request.Host)] {
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
		if cleaned == "/"+publication.ManifestName || cleaned == "/"+publication.UICatalogName {
			http.NotFound(writer, request)
			return
		}
		item, ok := assets[cleaned]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", item.ContentType)
		writer.Header().Set("ETag", item.ETag)
		if strings.HasSuffix(cleaned, "/index.html") || strings.HasSuffix(cleaned, "/bootstrap.json") || cleaned == "/"+publication.UICatalogName {
			writer.Header().Set("Cache-Control", "no-store")
		} else if item.Immutable {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		if request.Header.Get("If-None-Match") == item.ETag {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("Content-Length", fmt.Sprint(len(item.Data)))
		if request.Method == http.MethodGet {
			writeWithIdleDeadline(writer, item.Data)
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
