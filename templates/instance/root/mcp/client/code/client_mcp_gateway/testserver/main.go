// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

// testserver is a small MCP server for the end-to-end tests of the gateway: two harmless read tools over the
// streamable HTTP transport, on the loopback interface only. Test infrastructure, never deployed.
//
//	go run ./testserver -addr 127.0.0.1:18299
//	go run ./testserver -addr 127.0.0.1:18298 -oauth -token-life 30s   the same tools behind OAuth (oauth.go)
//	go run ./testserver -addr 127.0.0.1:18297 -bearer-file <file>      the same tools behind a static bearer token
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type customerIn struct {
	CustomerID string `json:"customer_id" jsonschema:"the identifier of the customer"`
}

type none struct{}

var customers = map[string]string{"42": "Ada Lovelace, London, customer since 2019", "7": "Alan Turing, Manchester, customer since 2021"}

func main() {
	addr := flag.String("addr", "127.0.0.1:18299", "loopback address to listen on")
	withOAuth := flag.Bool("oauth", false, "protect /mcp with OAuth and serve the authorization server on the same origin")
	tokenLife := flag.Duration("token-life", time.Hour, "lifetime of the access tokens the authorization server issues")
	bearerFile := flag.String("bearer-file", "", "protect /mcp with the static bearer token held in this file (the auth mode static of the registry)")
	flag.Parse()
	if host, _, err := net.SplitHostPort(*addr); err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatalf("testserver listens on a loopback address only, not %q", *addr)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "brique-test-crm", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "customer_read", Description: "Read one customer record by its identifier."},
		func(_ context.Context, _ *mcp.CallToolRequest, in customerIn) (*mcp.CallToolResult, none, error) {
			record, ok := customers[in.CustomerID]
			if !ok {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no customer " + in.CustomerID}}}, none{}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("customer %s: %s", in.CustomerID, record)}}}, none{}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "customer_list", Description: "List the identifiers of all customers."},
		func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "42, 7"}}}, none{}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	mux := http.NewServeMux()
	if *withOAuth {
		as := newAuthorizationServer("http://"+*addr, *tokenLife)
		as.register(mux)
		mux.Handle("/mcp", as.protect(handler))
		log.Printf("testserver with OAuth: issuer http://%s, client brique, tokens live %v", *addr, *tokenLife)
	} else if *bearerFile != "" {
		raw, err := os.ReadFile(*bearerFile)
		if err != nil {
			log.Fatalf("the bearer file: %v", err)
		}
		mux.Handle("/mcp", staticBearer(strings.TrimRight(string(raw), "\r\n"), handler))
		log.Printf("testserver with a static bearer token")
	} else {
		mux.Handle("/mcp", handler)
	}
	log.Printf("testserver on http://%s/mcp", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// staticBearer lets in only the requests that carry the token: the server side of the auth mode static.
func staticBearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
