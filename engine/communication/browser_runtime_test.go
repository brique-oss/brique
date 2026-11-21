/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"crypto/tls"
	"net/netip"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

func TestBrowserBridgeConfigurationIsExplicitAndStrict(t *testing.T) {
	if cfg := ParseCommCfg(nil); cfg.BrowserBridge.Declared || cfg.BrowserBridgeError != nil {
		t.Fatalf("absent bridge config = %+v, %v", cfg.BrowserBridge, cfg.BrowserBridgeError)
	}
	raw := map[string]any{"browser_bridge": map[string]any{
		"enabled": true, "allow_public_internet": true, "allow_cleartext_loopback": true,
		"allowed_prefixes": []any{"10.0.0.0/8", "fd00::/8"}, "max_addresses": float64(4),
		"open_timeout_ms": float64(2500), "_note": "ignored documentation",
	}}
	cfg := ParseCommCfg(raw)
	if cfg.BrowserBridgeError != nil || !cfg.BrowserBridge.Declared || !cfg.BrowserBridge.Enabled ||
		!cfg.BrowserBridge.AllowPublicInternet || !cfg.BrowserBridge.AllowCleartextLoopback ||
		cfg.BrowserBridge.MaxAddresses != 4 || cfg.BrowserBridge.OpenTimeout != 2500*time.Millisecond ||
		len(cfg.BrowserBridge.AllowedPrefixes) != 2 || cfg.BrowserBridge.AllowedPrefixes[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("parsed bridge config = %+v, %v", cfg.BrowserBridge, cfg.BrowserBridgeError)
	}
	invalid := []map[string]any{
		{"browser_bridge": true},
		{"browser_bridge": map[string]any{"enabled": "yes"}},
		{"browser_bridge": map[string]any{"enabled": true, "allow_intranet": true}},
		{"browser_bridge": map[string]any{"enabled": true, "allowed_prefixes": []any{"10.1.0.0/8"}}},
		{"browser_bridge": map[string]any{"enabled": true, "allowed_prefixes": []any{"10.0.0.0/8", "10.0.0.0/8"}}},
		{"browser_bridge": map[string]any{"enabled": true, "max_addresses": float64(0)}},
		{"browser_bridge": map[string]any{"enabled": true, "open_timeout_ms": float64(60001)}},
	}
	for index, value := range invalid {
		if parsed := ParseCommCfg(value); parsed.BrowserBridgeError == nil {
			t.Fatalf("invalid config %d accepted: %+v", index, parsed.BrowserBridge)
		}
	}
}

func TestConfiguredBrowserSinkFailsClosedWithoutBriqueSec(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	loop := NewCommLoop(&junction.ContextRegistry{CtxId: shared.RootContextID, CtxCommReg: registry}, map[string]any{
		"browser_bridge": map[string]any{"enabled": true, "allow_public_internet": true},
	})
	if enabled, err := loop.BrowserBridgeEnabled(); err != nil || !enabled {
		t.Fatalf("enabled = %v, %v", enabled, err)
	}
	if _, err := loop.NewConfiguredBrowserSink(func(string, []byte) error { return nil }, func(string) {}, nil); err == nil {
		t.Fatal("bridge started without Brique Sec")
	}
}

func TestConfiguredBrowserSinkComposesCurrentSecurityAndTransport(t *testing.T) {
	_, loop, _ := newSignedExternalTestLoop(t)
	loop.cfg.BrowserBridge = BrowserBridgeCfg{Declared: true, Enabled: true,
		AllowPublicInternet: true, MaxAddresses: 8, OpenTimeout: time.Second}
	loop.ingress = make(chan IngressItem, 1)
	loop.browserUI = make(map[string]func(circulation.Message) error)
	sink, err := loop.NewConfiguredBrowserSink(func(string, []byte) error { return nil }, func(string) {}, nil)
	if err != nil || sink == nil {
		t.Fatalf("configured sink = %v, %v", sink, err)
	}
	close(loop.done)
}

func TestBrowserBridgeTLSUsesVerifiedTLS13(t *testing.T) {
	config, err := browserBridgeTLSConfig("")
	if err != nil || config.MinVersion != tls.VersionTLS13 || config.InsecureSkipVerify || config.RootCAs == nil {
		t.Fatalf("TLS config = %+v, %v", config, err)
	}
}
