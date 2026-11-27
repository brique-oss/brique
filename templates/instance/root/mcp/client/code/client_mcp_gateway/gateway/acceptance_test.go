// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// Acceptance scenarios of the MCP design that no other test of the package covers (see mcp_acceptance.md).

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// "Secret is absent or ungranted": the server's preparation fails closed; an empty credential is never sent.
func TestAMissingSecretFailsClosed(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.servers["local"]["auth"] = map[string]any{"mode": "static", "secrets": map[string]any{"token": "crm_token"}}
	f.writeRegistry()
	f.open(nil) // the gateway received no secret
	out := f.project("local")
	unavailable := out["unavailable"].([]map[string]any)
	if len(unavailable) != 1 || unavailable[0]["reason"] != reasonAuthenticationNeeded {
		t.Fatalf("a server without its secret was prepared: %v", out)
	}
	plan, _ := f.gw.Call(context.Background(), CallRequest{CallID: "s1", Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": "x"}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
	if state(plan) != stateRefused || reason(plan) != reasonAuthenticationNeeded || server.calls.Load() != 0 {
		t.Fatalf("a call without its secret: %v", plan)
	}
}

// "OAuth grant is absent during preparation": the server's tools are omitted and the reason says a login is required.
func TestAnOAuthServerWithoutGrantIsLeftOutOfTheProjection(t *testing.T) {
	f := newOAuthFixture(t)
	f.open(nil)
	out := f.project("local")
	unavailable := out["unavailable"].([]map[string]any)
	if len(out["tools"].([]map[string]any)) != 0 || len(unavailable) != 1 || unavailable[0]["reason"] != reasonAuthenticationNeeded {
		t.Fatalf("projection without a grant: %v", out)
	}
}

// "Login completion has invalid ... state, issuer ...": the login refuses it and stores nothing.
func TestALoginWithATamperedAnswerStoresNothing(t *testing.T) {
	for name, tamper := range map[string]func(url.Values){
		"state":  func(v url.Values) { v.Set("state", "forged") },
		"issuer": func(v url.Values) { v.Set("iss", "https://evil.example") },
		"code":   func(v url.Values) { v.Set("code", "replayed-or-invented") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			_, err := Login(context.Background(), LoginConfig{StoreDir: f.dir, Alias: "local", Timeout: 5 * time.Second,
				Open: func(authorize string) {
					go func() {
						client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
						resp, err := client.Get(authorize)
						if err != nil {
							return
						}
						resp.Body.Close()
						back, _ := url.Parse(resp.Header.Get("Location"))
						values := back.Query()
						tamper(values)
						back.RawQuery = values.Encode()
						if resp, err := http.Get(back.String()); err == nil {
							resp.Body.Close()
						}
					}()
				}})
			if err == nil {
				t.Fatal("a tampered login succeeded")
			}
			if _, err := os.Stat(grantPath(f.dir, "local")); err == nil {
				t.Fatal("a grant was stored")
			}
			if f.mcp.toolCalls.Load() != 0 {
				t.Fatal("an MCP call was made")
			}
		})
	}
}

// "Two workers refresh the same rotating refresh token": a stale writer cannot overwrite the newer token set.
func TestAStaleWriterNeverReplacesANewerGrant(t *testing.T) {
	dir := t.TempDir()
	first := &Grant{Server: "crm", AccessToken: "a1", RefreshToken: "r1"}
	if err := writeGrant(dir, first, 0); err != nil {
		t.Fatal(err)
	}
	newer := *first
	newer.AccessToken, newer.RefreshToken = "a2", "r2"
	if err := writeGrant(dir, &newer, 1); err != nil {
		t.Fatal(err)
	}
	stale := *first
	stale.AccessToken, stale.RefreshToken = "a-stale", "r-stale"
	if err := writeGrant(dir, &stale, 1); err != errStaleGrant {
		t.Fatalf("a stale writer: %v", err)
	}
	stored, _ := readGrant(dir, "crm")
	if stored.RefreshToken != "r2" || stored.Revision != 2 {
		t.Fatalf("stored = %+v", stored)
	}
	if info, _ := os.Stat(grantPath(dir, "crm")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

// "Handler panics after transmission": the panic boundary commits the conservative state; status tells it.
func TestAPanicAfterTransmittingCommitsOutcomeUnknown(t *testing.T) {
	server := newFakeServer(t)
	f := newFixture(t, server.URL)
	f.open(nil)
	testHookTransmitting = func() { panic("boom after transmitting") }
	defer func() { testHookTransmitting = nil }()
	_, err := f.gw.Call(context.Background(), CallRequest{CallID: "p1", Origin: OriginPlan, Server: "local", Tool: "echo",
		Arguments: map[string]any{"text": "x"}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
	if err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic answer: %v", err)
	}
	status, _ := f.gw.Status("p1")
	if status["state"] != stateOutcomeUnknown || status["reason"] != reasonInternal {
		t.Fatalf("status after a panic: %v", status)
	}
}

// "One server is saturated": its excess calls are refused while another server goes on.
func TestOneSaturatedServerLeavesAnotherServed(t *testing.T) {
	busy, calm := newFakeServer(t), newFakeServer(t)
	f := newFixture(t, busy.URL)
	f.servers["local"]["limits"] = map[string]any{"concurrency": 1}
	f.servers["other"] = map[string]any{"transport": "streamable_http", "endpoint": calm.URL, "allow_private_network": true,
		"auth": map[string]any{"mode": "none"}, "catalog": map[string]any{"mode": "all"}}
	f.writeRegistry()
	f.open(nil)
	call := func(id, server, tool string) map[string]any {
		out, _ := f.gw.Call(context.Background(), CallRequest{CallID: id, Origin: OriginPlan, Server: server, Tool: tool,
			Arguments: map[string]any{"text": "x"}, CallerContext: "/root/shop", CallerCapacity: "order.sync"})
		return out
	}
	done := make(chan map[string]any)
	go func() { done <- call("b1", "local", "slow") }()
	<-busy.started
	if out := call("b2", "local", "echo"); reason(out) != reasonOverloaded {
		t.Fatalf("saturated server: %v", out)
	}
	if out := call("c1", "other", "echo"); state(out) != stateCompleted {
		t.Fatalf("the other server: %v", out)
	}
	busy.releaseAll()
	<-done
}

// "Login completion with ... redirect ...": a redirect_uri changed on the way sends the code elsewhere; the login receives nothing,
// gives up at its deadline and stores nothing. A person who never answers ends the same way.
func TestALoginWhoseRedirectIsDivertedOrNeverAnsweredStoresNothing(t *testing.T) {
	for name, open := range map[string]func(string){
		"diverted redirect": func(authorize string) {
			go func() {
				u, _ := url.Parse(authorize)
				q := u.Query()
				q.Set("redirect_uri", "http://127.0.0.1:1/callback") // the attacker's address
				u.RawQuery = q.Encode()
				client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				if resp, err := client.Get(u.String()); err == nil {
					resp.Body.Close()
				}
			}()
		},
		"never answered": func(string) {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			start := time.Now()
			_, err := Login(context.Background(), LoginConfig{StoreDir: f.dir, Alias: "local", Timeout: 2 * time.Second, Open: open})
			if err == nil || time.Since(start) > 10*time.Second {
				t.Fatalf("login: %v after %v", err, time.Since(start))
			}
			if _, err := os.Stat(grantPath(f.dir, "local")); err == nil {
				t.Fatal("a grant was stored")
			}
		})
	}
}
