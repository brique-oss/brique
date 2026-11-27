// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authServer is a small OAuth authorization server: Authorization Code with PKCE (S256), RFC 9207 iss, refresh-token
// rotation, and revocation of a grant.
type authServer struct {
	*httptest.Server
	issuer string

	mu        sync.Mutex
	codes     map[string]string    // code -> PKCE challenge
	access    map[string]time.Time // valid access tokens -> expiry
	refresh   string               // the current refresh token
	revoked   bool
	asked     []string // the scope parameter of each authorization request
	refreshes atomic.Int64
}

func token() string { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }

func newAuthServer(t *testing.T) *authServer {
	a := &authServer{codes: map[string]string{}, access: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"issuer": a.issuer, "authorization_endpoint": a.issuer + "/authorize", "token_endpoint": a.issuer + "/token",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
			"scopes_supported": []string{"read", "write", "admin", "offline_access"}, "token_endpoint_auth_methods_supported": []string{"none"},
			"authorization_response_iss_parameter_supported": true})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "brique" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		code := token()
		a.mu.Lock()
		a.codes[code] = q.Get("code_challenge")
		a.asked = append(a.asked, q.Get("scope"))
		a.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		values := back.Query()
		values.Set("code", code)
		values.Set("state", q.Get("state"))
		values.Set("iss", a.issuer)
		back.RawQuery = values.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound) // the person granted access at once
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		issue := func() {
			access, refresh := token(), token()
			a.access[access] = time.Now().Add(time.Hour)
			a.refresh = refresh
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": "read offline_access"})
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			challenge, ok := a.codes[r.Form.Get("code")]
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			delete(a.codes, r.Form.Get("code"))
			issue()
		case "refresh_token":
			a.refreshes.Add(1)
			if a.revoked || r.Form.Get("refresh_token") != a.refresh {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			issue()
		default:
			http.Error(w, "unsupported", http.StatusBadRequest)
		}
	})
	a.Server = httptest.NewServer(mux)
	a.issuer = a.Server.URL
	t.Cleanup(a.Server.Close)
	return a
}

func (a *authServer) valid(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.access[token]
	return ok && time.Now().Before(expiry)
}

func (a *authServer) forgetAccessTokens() {
	a.mu.Lock()
	a.access = map[string]time.Time{}
	a.mu.Unlock()
}

// protectedServer is an MCP server that asks for a bearer token of the authorization server.
type protectedServer struct {
	*httptest.Server
	toolCalls atomic.Int64
	posts     atomic.Int64 // tools/call requests that reached the HTTP layer, accepted or not
}

func newProtectedServer(t *testing.T, a *authServer) *protectedServer {
	p := &protectedServer{}
	server := mcp.NewServer(&mcp.Implementation{Name: "crm", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo."}, func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, none, error) {
		p.toolCalls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, none{}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"resource": p.URL + "/mcp", "authorization_servers": []string{a.issuer}, "scopes_supported": []string{"read", "write", "admin"}})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if strings.Contains(string(body), `"tools/call"`) {
			p.posts.Add(1)
		}
		if !a.valid(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, p.URL))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Server.Close)
	return p
}

type oauthFixture struct {
	*fixture
	as  *authServer
	mcp *protectedServer
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	a := newAuthServer(t)
	p := newProtectedServer(t, a)
	f := newFixture(t, p.URL+"/mcp")
	f.servers["local"]["auth"] = map[string]any{"mode": "oauth", "issuer": a.issuer, "client_id": "brique", "scopes": []string{"read"}}
	f.writeRegistry()
	return &oauthFixture{fixture: f, as: a, mcp: p}
}

func (f *oauthFixture) login() *Grant {
	f.t.Helper()
	grant, err := Login(context.Background(), LoginConfig{StoreDir: f.dir, Alias: "local", Timeout: 10 * time.Second,
		Open: func(u string) {
			go func() {
				resp, err := http.Get(u)
				if err == nil {
					resp.Body.Close()
				}
			}()
		}})
	if err != nil {
		f.t.Fatalf("login: %v", err)
	}
	return grant
}

// expireGrant makes the stored access token old, as time does.
func (f *oauthFixture) expireGrant() {
	f.t.Helper()
	g, err := readGrant(f.dir, "local")
	if err != nil {
		f.t.Fatal(err)
	}
	g.Expiry = time.Now().Add(-time.Minute)
	if err := writeGrant(f.dir, g, g.Revision); err != nil {
		f.t.Fatal(err)
	}
}

func (f *oauthFixture) call(id string) map[string]any {
	f.t.Helper()
	out, err := f.gw.Call(context.Background(), CallRequest{CallID: id, Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": id}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestLoginStoresAGrantWithinTheScopeCeiling(t *testing.T) {
	f := newOAuthFixture(t)
	grant := f.login()
	if grant.RefreshToken == "" || grant.Revision != 1 || grant.Issuer != f.as.issuer {
		t.Fatalf("grant = %+v", grant)
	}
	if len(f.as.asked) != 1 || strings.Contains(f.as.asked[0], "write") || strings.Contains(f.as.asked[0], "admin") || !strings.Contains(f.as.asked[0], "read") {
		t.Fatalf("scopes asked: %v", f.as.asked)
	}
	info, err := os.Stat(grantPath(f.dir, "local"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("grant file: %v %v", info, err)
	}
}

func TestLoginRefusesAnotherIssuer(t *testing.T) {
	f := newOAuthFixture(t)
	f.servers["local"]["auth"] = map[string]any{"mode": "oauth", "issuer": "http://127.0.0.1:1", "client_id": "brique"}
	f.writeRegistry()
	if _, err := Login(context.Background(), LoginConfig{StoreDir: f.dir, Alias: "local", Timeout: 5 * time.Second,
		Open: func(u string) { go http.Get(u) }}); err == nil {
		t.Fatal("a login to another issuer succeeded")
	}
	if _, err := os.Stat(grantPath(f.dir, "local")); err == nil {
		t.Fatal("a grant was stored")
	}
}

func TestTheGatewayUsesTheGrantAndRefreshesItOnceForConcurrentCalls(t *testing.T) {
	f := newOAuthFixture(t)
	f.login()
	f.open(nil)
	if out := f.call("o1"); state(out) != stateCompleted {
		t.Fatalf("call with the grant: %v", out)
	}
	f.expireGrant() // revision 2: the gateway refreshes before use
	var wg sync.WaitGroup
	outs := make([]map[string]any, 5)
	for i := range outs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); outs[i] = f.call(fmt.Sprintf("o%d", i+2)) }(i)
	}
	wg.Wait()
	for _, out := range outs {
		if state(out) != stateCompleted {
			t.Fatalf("call after expiry: %v", out)
		}
	}
	grant, _ := readGrant(f.dir, "local")
	if f.as.refreshes.Load() != 1 || grant.Revision != 3 {
		t.Fatalf("refreshes = %d, revision = %d", f.as.refreshes.Load(), grant.Revision)
	}
}

func TestARevokedGrantIsAuthenticationRequiredBeforeAnythingIsSent(t *testing.T) {
	f := newOAuthFixture(t)
	f.login()
	f.open(nil)
	f.call("warm")
	f.as.revoked = true
	f.expireGrant()
	before := f.mcp.posts.Load()
	out := f.call("r1")
	// the cached listing is old too: the call is refused while the server is prepared, before any call exists
	if (state(out) != stateRefused && state(out) != stateFailedBeforeTransmit) || reason(out) != reasonAuthenticationNeeded || out["transmission"] != transmissionNone {
		t.Fatalf("revoked grant: %v", out)
	}
	if f.mcp.posts.Load() != before {
		t.Fatal("a tools/call was sent without a usable token")
	}
}

func TestA401DuringACallIsNeverSentAgain(t *testing.T) {
	f := newOAuthFixture(t)
	f.login()
	f.open(nil)
	f.call("warm")
	f.as.forgetAccessTokens() // the server stops accepting the token the gateway still believes valid
	before, calls := f.mcp.posts.Load(), f.mcp.toolCalls.Load()
	out := f.call("u1")
	if state(out) != stateRemoteError || reason(out) != reasonAuthenticationNeeded {
		t.Fatalf("401 on a call: %v", out)
	}
	if f.mcp.posts.Load()-before != 1 || f.mcp.toolCalls.Load() != calls {
		t.Fatalf("tools/call sent %d times, executed %d", f.mcp.posts.Load()-before, f.mcp.toolCalls.Load()-calls)
	}
}

func TestLogoutAndAChangedEntryLeaveNoUsableGrant(t *testing.T) {
	f := newOAuthFixture(t)
	f.login()
	f.open(nil)
	f.call("warm")
	f.servers["local"]["auth"] = map[string]any{"mode": "oauth", "issuer": f.as.issuer, "client_id": "another", "scopes": []string{"read"}}
	f.writeRegistry()
	if out := f.call("c1"); reason(out) != reasonAuthenticationNeeded {
		t.Fatalf("a grant used for another client: %v", out)
	}
	if err := Logout(f.dir, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(grantPath(f.dir, "local")); err == nil {
		t.Fatal("logout left the grant")
	}
	if err := Logout(f.dir, "local"); err == nil {
		t.Fatal("a second logout succeeded")
	}
}

func TestOAuthRegistryEntriesAreValidated(t *testing.T) {
	base := `{"format":"brique-mcp-registry","version":1,"servers":{"s":{"transport":"streamable_http","endpoint":"https://mcp.example.com/mcp","auth":%s,"catalog":{"mode":"all"}}}}`
	good := `{"mode":"oauth","issuer":"https://as.example.com","client_id":"brique","scopes":["read"],"secrets":{"client_secret":"crm_client"}}`
	if _, err := parseRegistry([]byte(fmt.Sprintf(base, good))); err != nil {
		t.Fatalf("a valid oauth entry was refused: %v", err)
	}
	for name, a := range map[string]string{
		"no issuer":        `{"mode":"oauth","client_id":"brique"}`,
		"http issuer":      `{"mode":"oauth","issuer":"http://as.example.com","client_id":"brique"}`,
		"no client":        `{"mode":"oauth","issuer":"https://as.example.com"}`,
		"other secret":     `{"mode":"oauth","issuer":"https://as.example.com","client_id":"b","secrets":{"token":"t"}}`,
		"low port":         `{"mode":"oauth","issuer":"https://as.example.com","client_id":"b","redirect_port":80}`,
		"bad scope":        `{"mode":"oauth","issuer":"https://as.example.com","client_id":"b","scopes":["a b"]}`,
		"issuer on static": `{"mode":"static","secrets":{"token":"t"},"issuer":"https://as.example.com"}`,
	} {
		if _, err := parseRegistry([]byte(fmt.Sprintf(base, a))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
