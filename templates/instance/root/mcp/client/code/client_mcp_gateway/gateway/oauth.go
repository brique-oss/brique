// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// oauth.go
//
// The OAuth grant of a server whose registry entry says auth "oauth": one grant held by the instance for that server,
// obtained by the operator's login (login.go) and kept in the private store (oauth/<alias>.json, owner-only, written
// atomically, with a revision so that a stale writer never replaces a newer token set). At runtime the gateway gives the
// SDK a handler that only reads the grant and refreshes it: one refresh at a time per server, the new token set committed
// before it is used. Its Authorize never runs a flow and always fails, so the SDK never sends a request twice after a 401
// or 403: a call refused for authentication is answered authentication_required and the operator logs in again.
//
// A grant belongs to its server entry: if the registry changes the endpoint, the issuer, the client or the scope ceiling,
// the grant no longer applies (authentication_required) until a new login. Token values never leave this file and the
// SDK transport: not in a journal, a log, an error or a result.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/oauth2"
)

const (
	grantsDir     = "oauth"
	grantFormat   = "brique-mcp-grant"
	refreshMargin = 30 * time.Second // a token this close to its expiry is refreshed before use
	maxGrantBytes = 64 << 10
)

// errAuthenticationRequired: no usable grant (none, revoked, expired without refresh, bound to another configuration).
var errAuthenticationRequired = errors.New("authentication required: the operator must log in to this server")

// Grant is the stored authorization of one server.
type Grant struct {
	Format       string    `json:"format"`
	Revision     int64     `json:"revision"`
	Server       string    `json:"server"`
	Endpoint     string    `json:"endpoint"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"client_id"`
	TokenURL     string    `json:"token_url"`
	AuthStyle    int       `json:"auth_style"`
	Scopes       []string  `json:"scopes"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	ObtainedAt   time.Time `json:"obtained_at"`
}

func (g *Grant) token() *oauth2.Token {
	return &oauth2.Token{AccessToken: g.AccessToken, RefreshToken: g.RefreshToken, TokenType: g.TokenType, Expiry: g.Expiry}
}

// matches tells whether the grant was obtained for this registry entry as it is now.
func (g *Grant) matches(server *Server) bool {
	if g.Server != server.alias || g.Endpoint != server.Endpoint || g.Issuer != server.Auth.Issuer || g.ClientID != server.Auth.ClientID {
		return false
	}
	for _, scope := range g.Scopes {
		if scope != "offline_access" && len(server.Auth.Scopes) > 0 && !slices.Contains(server.Auth.Scopes, scope) { // offline_access only asks for a refresh token
			return false
		}
	}
	return true
}

func grantPath(storeDir, alias string) string {
	return filepath.Join(storeDir, grantsDir, alias+".json")
}

// readGrant reads a grant with the checks of a protected file: a regular file, not a link, owner-only, bounded.
func readGrant(storeDir, alias string) (*Grant, error) {
	path := grantPath(storeDir, alias)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxGrantBytes {
		return nil, fmt.Errorf("grant of %s is not an owner-only regular file", alias)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Grant
	if err := json.Unmarshal(raw, &g); err != nil || g.Format != grantFormat {
		return nil, fmt.Errorf("grant of %s is unreadable", alias)
	}
	return &g, nil
}

// writeGrant replaces the grant atomically and durably, unless the stored revision is no longer the one the writer read
// (expected; 0 for a new grant): then the stored, newer grant is kept and returned with errStaleGrant.
var errStaleGrant = errors.New("the stored grant changed meanwhile")

var grantWriteMu sync.Mutex // the writers of one process; another process (login) is caught by the revision

func writeGrant(storeDir string, g *Grant, expected int64) error {
	grantWriteMu.Lock()
	defer grantWriteMu.Unlock()
	dir := filepath.Join(storeDir, grantsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if current, err := readGrant(storeDir, g.Server); err == nil && current.Revision != expected {
		return errStaleGrant
	} else if err != nil && !errors.Is(err, os.ErrNotExist) && expected != 0 {
		return err
	}
	g.Format, g.Revision = grantFormat, expected+1
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	path := grantPath(storeDir, g.Server)
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// newOAuthClient is the HTTP client of every OAuth request (discovery, code exchange, refresh): the egress controls of the
// gateway (no proxy, no redirect, the dialed address checked, bounded bodies), and no credential of the MCP server.
func newOAuthClient(server *Server) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	if !server.AllowPrivateNetwork {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil || blockedIP(net.ParseIP(host)) {
				return errBlockedAddress
			}
			return nil
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true, TLSHandshakeTimeout: tlsTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout, MaxResponseHeaderBytes: maxResponseHeaderSize}
	return &http.Client{
		Timeout:       30 * time.Second,
		Transport:     &oauthTransport{base: transport, allowHTTP: server.AllowPrivateNetwork},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}
}

type oauthTransport struct {
	base      http.RoundTripper
	allowHTTP bool
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && !(t.allowHTTP && req.URL.Scheme == "http") {
		return nil, errors.New("an OAuth endpoint must use https")
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &boundedBody{body: resp.Body, left: maxGrantBytes}
	return resp, nil
}

// grantHandler is the SDK's OAuthHandler of one server at runtime.
type grantHandler struct {
	storeDir string
	server   *Server
	secrets  map[string]string
	client   *http.Client
	now      func() time.Time

	mu sync.Mutex // one refresh at a time for this server
}

func newGrantHandler(storeDir string, server *Server, secrets map[string]string, now func() time.Time) *grantHandler {
	return &grantHandler{storeDir: storeDir, server: server, secrets: secrets, client: newOAuthClient(server), now: now}
}

// TokenSource gives the SDK a source that reads the current grant at each request.
func (h *grantHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h, nil
}

// Authorize never runs a flow at runtime and always fails: the SDK then does not send the request again.
func (h *grantHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
	return errAuthenticationRequired
}

// Token is the current access token, refreshed first when it is about to expire.
func (h *grantHandler) Token() (*oauth2.Token, error) {
	grant, err := readGrant(h.storeDir, h.server.alias)
	if err != nil || !grant.matches(h.server) {
		return nil, errAuthenticationRequired
	}
	if grant.Expiry.IsZero() || h.now().Add(refreshMargin).Before(grant.Expiry) {
		return grant.token(), nil
	}
	return h.refresh()
}

func (h *grantHandler) refresh() (*oauth2.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	grant, err := readGrant(h.storeDir, h.server.alias) // a concurrent caller may have refreshed it while this one waited
	if err != nil || !grant.matches(h.server) {
		return nil, errAuthenticationRequired
	}
	if grant.Expiry.IsZero() || h.now().Add(refreshMargin).Before(grant.Expiry) {
		return grant.token(), nil
	}
	if grant.RefreshToken == "" {
		return nil, errAuthenticationRequired
	}
	config := &oauth2.Config{ClientID: grant.ClientID, ClientSecret: h.secrets[h.server.Auth.Secrets["client_secret"]],
		Endpoint: oauth2.Endpoint{TokenURL: grant.TokenURL, AuthStyle: oauth2.AuthStyle(grant.AuthStyle)}, Scopes: grant.Scopes}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), oauth2.HTTPClient, h.client), 30*time.Second)
	defer cancel()
	fresh, err := config.TokenSource(ctx, &oauth2.Token{RefreshToken: grant.RefreshToken}).Token()
	if err != nil {
		return nil, errAuthenticationRequired // invalid_grant, revoked consent, unreachable issuer: never a fallback
	}
	next := *grant
	next.AccessToken, next.TokenType, next.Expiry, next.ObtainedAt = fresh.AccessToken, fresh.TokenType, fresh.Expiry, h.now().UTC()
	if fresh.RefreshToken != "" {
		next.RefreshToken = fresh.RefreshToken // rotation
	}
	if err := writeGrant(h.storeDir, &next, grant.Revision); err != nil {
		if errors.Is(err, errStaleGrant) { // a login replaced the grant meanwhile: it wins
			if current, readErr := readGrant(h.storeDir, h.server.alias); readErr == nil && current.matches(h.server) {
				return current.token(), nil
			}
		}
		return nil, errAuthenticationRequired // the new token set could not be committed: it is not used
	}
	return fresh, nil
}
