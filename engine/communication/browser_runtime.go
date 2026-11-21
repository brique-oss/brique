/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/netip"
	"os"
	"path/filepath"

	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
	"brique_engine/engine/transport/browserclient"
	"brique_engine/engine/transport/networkpolicy"
	"brique_engine/platform/protectedfile"
)

var ErrBrowserBridgeDisabled = errors.New("browser bridge is not enabled")

func (loop *CommLoop) BrowserBridgeEnabled() (bool, error) {
	if loop == nil || loop.frame == nil || !shared.IsRootContext(loop.frame.CtxId) {
		return false, ErrBrowserSink
	}
	if loop.cfg.BrowserBridgeError != nil {
		return false, loop.cfg.BrowserBridgeError
	}
	return loop.cfg.BrowserBridge.Declared && loop.cfg.BrowserBridge.Enabled, nil
}

// NewConfiguredBrowserSink composes the root Comm loop with current Brique Sec,
// the transport hub and the explicit local destination policy. The native host
// receives no part of those objects.
func (loop *CommLoop) NewConfiguredBrowserSink(deliver func(string, []byte) error,
	invalidate func(string), channelClosed func(string)) (*BrowserSink, error) {
	enabled, err := loop.BrowserBridgeEnabled()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrBrowserBridgeDisabled
	}
	securityProvider, ok := loop.frame.CtxCommReg.(junction.SecurityRuntimeProvider)
	if !ok || securityProvider.SecurityRuntime() == nil {
		return nil, errors.New("browser bridge requires an active Brique Sec runtime")
	}
	hubProvider, ok := loop.frame.CtxCommReg.(junction.SignedTransportProvider)
	if !ok || hubProvider.SignedTransport() == nil {
		return nil, errors.New("browser bridge signed transport is unavailable")
	}
	network, err := networkpolicy.New(networkpolicy.Options{
		AllowPublicInternet:    loop.cfg.BrowserBridge.AllowPublicInternet,
		AllowCleartextLoopback: loop.cfg.BrowserBridge.AllowCleartextLoopback,
		AllowedPrefixes:        append([]netip.Prefix(nil), loop.cfg.BrowserBridge.AllowedPrefixes...),
		MaxAddresses:           loop.cfg.BrowserBridge.MaxAddresses,
	})
	if err != nil {
		return nil, err
	}
	machineDirectory := ""
	if provider, ok := loop.frame.CtxCommReg.(junction.MachineDirectoryProvider); ok {
		machineDirectory = provider.MachineDirectory()
	}
	lifetime, cancel := context.WithCancel(context.Background())
	connector, err := browserclient.New(browserclient.Options{Runtime: securityProvider.SecurityRuntime,
		Hub: hubProvider.SignedTransport(), Network: network, Lifetime: lifetime,
		TLSConfig: func() (*tls.Config, error) { return browserBridgeTLSConfig(machineDirectory) }})
	if err != nil {
		cancel()
		return nil, err
	}
	go func() {
		<-loop.done
		cancel()
	}()
	return NewBrowserSink(BrowserSinkOptions{Loop: loop, Connect: BrowserConnectWith(connector),
		AdmitLocal: securityProvider.SecurityRuntime().Trust().AdmitLocalUI,
		Deliver:    deliver, Invalidate: invalidate, ChannelClosed: channelClosed,
		OpenTimeout: loop.cfg.BrowserBridge.OpenTimeout})
}

const maxBrowserTrustedPEMBytes = 4 << 20

func browserBridgeTLSConfig(machineDirectory string) (*tls.Config, error) {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if machineDirectory != "" {
		trustedPath := filepath.Join(machineDirectory, "tls", "trusted.pem")
		if _, err := os.Stat(trustedPath); err == nil {
			content, err := protectedfile.ReadProtectedFile(machineDirectory, "tls/trusted.pem",
				protectedfile.ProtectedFileOptions{MaxBytes: maxBrowserTrustedPEMBytes})
			if err != nil || !roots.AppendCertsFromPEM(content) {
				return nil, errors.New("browser bridge trusted certificates are invalid")
			}
		} else if !os.IsNotExist(err) {
			return nil, errors.New("browser bridge trusted certificates are unavailable")
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, nil
}
