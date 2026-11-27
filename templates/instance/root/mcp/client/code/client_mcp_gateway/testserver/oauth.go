// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

// oauth.go — an OAuth authorization server for the end-to-end tests of the gateway's OAuth (test infrastructure, loopback only):
// Authorization Code with PKCE (S256) for the pre-registered public client "brique", the RFC 9207 iss in the redirect,
// scopes read and write, refresh tokens that rotate, short-lived access tokens, and an administrative revocation
// (POST /admin/revoke) that invalidates every token. The person's consent is given at once: /authorize redirects back.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type authorizationServer struct {
	issuer string
	life   time.Duration

	mu      sync.Mutex
	codes   map[string]string    // code -> PKCE challenge
	access  map[string]time.Time // access token -> expiry
	refresh map[string]bool      // refresh tokens still valid
	issued  []string             // every token ever issued, access and refresh: what a test must never find outside the grant
	stats   struct{ authorizations, exchanges, refreshes, refused int }
}

func newAuthorizationServer(issuer string, life time.Duration) *authorizationServer {
	return &authorizationServer{issuer: issuer, life: life, codes: map[string]string{}, access: map[string]time.Time{}, refresh: map[string]bool{}}
}

func random() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *authorizationServer) register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"issuer": a.issuer, "authorization_endpoint": a.issuer + "/authorize",
			"token_endpoint": a.issuer + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
			"grant_types_supported": []string{"authorization_code", "refresh_token"}, "scopes_supported": []string{"read", "write", "offline_access"},
			"token_endpoint_auth_methods_supported": []string{"none"}, "authorization_response_iss_parameter_supported": true})
	})
	resource := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"resource": a.issuer + "/mcp", "authorization_servers": []string{a.issuer}, "scopes_supported": []string{"read", "write"}})
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", resource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", resource)
	mux.HandleFunc("/authorize", a.authorize)
	mux.HandleFunc("/token", a.token)
	mux.HandleFunc("/admin/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		a.mu.Lock()
		a.access, a.refresh = map[string]time.Time{}, map[string]bool{}
		a.mu.Unlock()
		log.Printf("oauth: every token revoked")
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})
	mux.HandleFunc("/admin/issued", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"tokens": append([]string{}, a.issued...)})
	})
	mux.HandleFunc("/admin/stats", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"authorizations": a.stats.authorizations, "exchanges": a.stats.exchanges,
			"refreshes": a.stats.refreshes, "refused": a.stats.refused})
	})
}

func (a *authorizationServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != "brique" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		redirect.Hostname() != "127.0.0.1" || redirect.Path != "/callback" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	code := random()
	a.mu.Lock()
	a.codes[code] = q.Get("code_challenge")
	a.stats.authorizations++
	a.mu.Unlock()
	log.Printf("oauth: authorization granted, scope %q", q.Get("scope"))
	values := redirect.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	values.Set("iss", a.issuer)
	redirect.RawQuery = values.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (a *authorizationServer) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	a.mu.Lock()
	defer a.mu.Unlock()
	issue := func() {
		access, refresh := random(), random()
		a.access[access] = time.Now().Add(a.life)
		a.refresh[refresh] = true
		a.issued = append(a.issued, access, refresh)
		writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer",
			"expires_in": int(a.life.Seconds()), "scope": "read offline_access"})
	}
	refuse := func() {
		a.stats.refused++
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		challenge, ok := a.codes[r.Form.Get("code")]
		delete(a.codes, r.Form.Get("code")) // a code is used once
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		client := r.Form.Get("client_id")
		if user, _, basic := r.BasicAuth(); basic { // a public client may name itself in the Authorization header (RFC 6749 2.3.1)
			client = user
		}
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || client != "brique" {
			refuse()
			return
		}
		a.stats.exchanges++
		issue()
	case "refresh_token":
		presented := r.Form.Get("refresh_token")
		if !a.refresh[presented] {
			refuse()
			return
		}
		delete(a.refresh, presented) // rotation: a refresh token is used once
		a.stats.refreshes++
		log.Printf("oauth: token refreshed")
		issue()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
	}
}

// protect asks for a valid access token; without one the answer points to the protected resource metadata.
func (a *authorizationServer) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.mu.Lock()
		expiry, ok := a.access[token]
		a.mu.Unlock()
		if !ok || !time.Now().Before(expiry) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, a.issuer))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
