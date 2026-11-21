/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"brique_engine/webhost"
)

type repeatedFlag []string

func (values *repeatedFlag) String() string { return fmt.Sprint([]string(*values)) }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	publication := flag.String("publication", "", "immutable Spark web publication directory")
	address := flag.String("addr", webhost.DefaultAddress, "loopback HTTP listen address")
	publicWebBackend := flag.String("public_web_backend", "", "engine-owned loopback public WebSocket endpoint")
	var allowedHosts repeatedFlag
	var publicOrigins repeatedFlag
	var trustedProxyAddresses repeatedFlag
	flag.Var(&allowedHosts, "allowed_host", "exact public Host authority accepted behind the reverse proxy (repeatable)")
	flag.Var(&publicOrigins, "public_origin", "exact HTTPS browser origin accepted behind the reverse proxy (repeatable)")
	flag.Var(&trustedProxyAddresses, "trusted_proxy", "exact reverse-proxy IP allowed to provide X-Brique-Client-Address (repeatable)")
	supervisorLifetimeHandle := flag.String("supervisor_lifetime_handle", "", "inherited launcher lifetime handle")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "brique-web: unexpected arguments")
		os.Exit(2)
	}
	server, err := webhost.Start(webhost.Config{Address: *address, PublicationDirectory: *publication,
		AllowedHosts: allowedHosts, PublicOrigins: publicOrigins, TrustedProxyAddresses: trustedProxyAddresses,
		PublicWebBackend: *publicWebBackend})
	if err != nil {
		fmt.Fprintf(os.Stderr, "brique-web: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Brique web host ready: %s\n", server.URL())
	lifetimeLost, closeLifetime, err := startSupervisorLifetime(*supervisorLifetimeHandle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brique-web: supervisor lifetime: %v\n", err)
		os.Exit(1)
	}
	defer closeLifetime()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case <-signals:
	case <-lifetimeLost:
	}
	signal.Stop(signals)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "brique-web: shutdown: %v\n", err)
		os.Exit(1)
	}
}
