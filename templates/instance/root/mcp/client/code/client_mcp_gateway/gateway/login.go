// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// login.go
//
// The operator's login to an OAuth server of the registry, outside any request: a command run on the machine
// (client_mcp_gateway login <alias> --store <dir>). The SDK's Authorization Code handler does the protocol: discovery of
// the protected resource and of the authorization server, PKCE, state, the RFC 9207 issuer check, the code exchange. Brique
// gives it what it must not choose: the client registered beforehand and bound to the issuer of the registry, the scopes
// cut to the ceiling of the registry, the egress-controlled HTTP client, and a callback served on the loopback interface for
// this login only. The token set the exchange yields is written to the private store; the running gateway reads it at its
// next request. Nothing listens once the command ends.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// LoginConfig of one login.
type LoginConfig struct {
	StoreDir string               // the private store of the gateway (where registry.json is installed)
	Alias    string               // the server of the registry
	Secrets  map[string]string    // machine secrets by name (client_secret)
	Open     func(url string)     // shows the authorization URL to the person (prints it by default)
	Timeout  time.Duration        // how long the person has to grant access (5 minutes by default)
	Log      func(string, ...any) // progress lines for the operator
}

// Login runs the Authorization Code flow with PKCE for one server and stores the grant.
func Login(ctx context.Context, cfg LoginConfig) (*Grant, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	reg, err := newRegistryStore(cfg.StoreDir).get()
	if err != nil {
		return nil, fmt.Errorf("the installed registry: %w", err)
	}
	server := reg.Servers[cfg.Alias]
	if server == nil || server.Auth.Mode != "oauth" {
		return nil, fmt.Errorf("server %q is not an oauth server of the installed registry", cfg.Alias)
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(server.Auth.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("the loopback callback: %w", err)
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	results := make(chan *auth.AuthorizationResult, 1)
	callback := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if problem := query.Get("error"); problem != "" {
			http.Error(w, "the authorization server refused: "+problem, http.StatusBadRequest)
			results <- &auth.AuthorizationResult{}
			return
		}
		select {
		case results <- &auth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state"), Iss: query.Get("iss")}:
			fmt.Fprintln(w, "Brique: the login is complete, this window can be closed.")
		default:
			http.Error(w, "this login already received its answer", http.StatusConflict)
		}
	})}
	go callback.Serve(listener)
	defer callback.Close()

	open := cfg.Open
	if open == nil {
		open = func(url string) {
			fmt.Fprintf(os.Stderr, "Open this URL in a browser to grant access to %s:\n%s\n", cfg.Alias, url)
		}
	}
	var exchanged *Grant
	fetched := false // one login is one authorization: a second request for consent means the first failed
	client := &oauthex.ClientCredentials{ClientID: server.Auth.ClientID, Issuer: server.Auth.Issuer}
	if name := server.Auth.Secrets["client_secret"]; name != "" {
		secret := cfg.Secrets[name]
		if secret == "" {
			return nil, fmt.Errorf("secret %q of server %s is not available", name, cfg.Alias)
		}
		client.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: secret}
	}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: client,
		RedirectURL:         redirect,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if fetched {
				return nil, errors.New("the authorization server did not accept the first authorization (code exchange refused): see its logs; nothing is stored")
			}
			fetched = true
			open(args.URL)
			select {
			case result := <-results:
				if result.Code == "" {
					return nil, errors.New("no authorization code was received")
				}
				return result, nil
			case <-ctx.Done():
				return nil, errors.New("no answer from the browser before the login timed out")
			}
		},
		ScopeFilter:         func(discovered []string) []string { return withinCeiling(discovered, server.Auth.Scopes) },
		RequestRefreshToken: true,
		Client:              newOAuthClient(server),
		NewTokenSource: func(ctx context.Context, config *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			scopes := grantedScopes(token, config.Scopes)
			if outside := outsideCeiling(scopes, server.Auth.Scopes); len(outside) > 0 {
				return nil, fmt.Errorf("the authorization server granted scopes beyond the ceiling of the registry: %s", strings.Join(outside, " "))
			}
			exchanged = &Grant{Server: cfg.Alias, Endpoint: server.Endpoint, Issuer: server.Auth.Issuer, ClientID: config.ClientID,
				TokenURL: config.Endpoint.TokenURL, AuthStyle: int(config.Endpoint.AuthStyle), Scopes: scopes,
				AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType, Expiry: token.Expiry,
				ObtainedAt: time.Now().UTC()}
			return oauth2.StaticTokenSource(token), nil
		},
	})
	if err != nil {
		return nil, err
	}
	// Reaching the server without a token makes it ask for one: the handler then runs the flow (this is the only place
	// where Authorize may run interactively). The session that follows proves the grant works.
	cfg.Log("connecting to %s", server.Endpoint)
	transport := &mcp.StreamableClientTransport{Endpoint: server.Endpoint, HTTPClient: newServerClient(server, nil), OAuthHandler: handler,
		DisableStandaloneSSE: true, MaxRetries: -1}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "brique-mcp-gateway-login", Version: "1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}).Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("login to %s: %w", cfg.Alias, err)
	}
	defer session.Close()
	if exchanged == nil {
		return nil, fmt.Errorf("login to %s: the server let the client in without asking for a token: nothing to store", cfg.Alias)
	}
	if _, err := session.ListTools(ctx, nil); err != nil {
		return nil, fmt.Errorf("login to %s: the new grant does not list the tools: %w", cfg.Alias, err)
	}
	previous := int64(0)
	if current, err := readGrant(cfg.StoreDir, cfg.Alias); err == nil {
		previous = current.Revision
	}
	if err := writeGrant(cfg.StoreDir, exchanged, previous); err != nil {
		return nil, fmt.Errorf("the grant could not be stored: %w", err)
	}
	return exchanged, nil
}

// Logout deletes the grant of a server; the next call to it is refused with authentication_required.
func Logout(storeDir, alias string) error {
	if !aliasPattern.MatchString(alias) {
		return errors.New("not a server alias")
	}
	err := os.Remove(grantPath(storeDir, alias))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no grant is stored for %s", alias)
	}
	return err
}

// withinCeiling keeps the discovered scopes the registry allows; with no ceiling the discovered set is asked as it is.
func withinCeiling(discovered, ceiling []string) []string {
	if len(ceiling) == 0 {
		return discovered
	}
	kept := []string{}
	for _, scope := range discovered {
		if slices.Contains(ceiling, scope) {
			kept = append(kept, scope)
		}
	}
	if len(kept) == 0 {
		return append([]string(nil), ceiling...) // the server advertised none of them: ask for the ceiling itself
	}
	return kept
}

func outsideCeiling(scopes, ceiling []string) []string {
	if len(ceiling) == 0 {
		return nil
	}
	var outside []string
	for _, scope := range scopes {
		if scope != "offline_access" && !slices.Contains(ceiling, scope) {
			outside = append(outside, scope)
		}
	}
	return outside
}

// grantedScopes are the scopes the token response states, or the ones asked when it states none.
func grantedScopes(token *oauth2.Token, asked []string) []string {
	if raw, ok := token.Extra("scope").(string); ok && raw != "" {
		return strings.Fields(raw)
	}
	return append([]string(nil), asked...)
}

// MachineSecret reads a machine secret for the login command: from MCP_SECRET_<NAME> when the operator set it, else from
// the secrets folder of the machine directory with the checks of a protected file.
func MachineSecret(storeDir, name string) (string, error) {
	if value := os.Getenv(secretVariable(name)); value != "" {
		return value, nil
	}
	if !secretNamePattern.MatchString(name) {
		return "", errors.New("not a secret name")
	}
	path := filepath.Join(filepath.Dir(filepath.Dir(storeDir)), "secrets", name) // <machine>/wrappers/<wrapper> -> <machine>/secrets/<name>
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64<<10 {
		return "", fmt.Errorf("secret %q is not an owner-only regular file", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// ClientSecretName is the name of the machine secret the installed registry names as client_secret of a server.
func ClientSecretName(storeDir, alias string) (string, error) {
	reg, err := newRegistryStore(storeDir).get()
	if err != nil {
		return "", err
	}
	server := reg.Servers[alias]
	if server == nil {
		return "", fmt.Errorf("unknown server %q", alias)
	}
	return server.Auth.Secrets["client_secret"], nil
}
