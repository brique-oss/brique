/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"

	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/engine/junction"
	"brique_engine/engine/transport/diagnostic"
)

// DiagnoseEndpoint probes one endpoint of a declared peer with this interface's own client
// settings: the roots of its real client (system plus tls_ca_file or the machine's trusted
// certificates, as they are now), its minimum TLS version, and the refusal a signed send
// makes when verification is off. The request goes to the path a real send uses.
func (h *HttpsInterface) DiagnoseEndpoint(ctx context.Context, peerID, endpointName, target string) connectiondiagnosis.Result {
	refuse := func(step, code, message string) connectiondiagnosis.Result {
		return connectiondiagnosis.Refused(peerID, endpointName, step, code, message)
	}
	if h == nil || !h.egressEnabled {
		return refuse(connectiondiagnosis.StepConfiguration, connectiondiagnosis.CodeNoHTTPSInterface, "this interface does not send")
	}
	var tlsConfig *tls.Config
	if transport, ok := h.httpClient().Transport.(*http.Transport); ok && transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	source := "system"
	if h.caFile != "" || h.trustedFile != "" {
		source = "system+file"
	}
	path := "/msg"
	if parsed, err := url.Parse(target); err == nil {
		path = strings.TrimRight(parsed.Path, "/") + "/msg"
	}
	return diagnostic.Run(ctx, diagnostic.Input{
		PeerID: peerID, Endpoint: endpointName, Target: target,
		TLS: tlsConfig, TrustSource: source,
		VerificationDisabled: h.insecureSkipVerify || strings.TrimSpace(h.serverName) != "",
		HealthPath:           path,
	})
}

var _ junction.ConnectionDiagnoser = (*HttpsInterface)(nil).DiagnoseEndpoint

// connectionDiagnosable is what an interface implementation offers to be diagnosed.
type connectionDiagnosable interface {
	DiagnoseEndpoint(ctx context.Context, peerID, endpointName, target string) connectiondiagnosis.Result
}

// registerDiagnosers announces the interfaces that can be diagnosed to the registry; the
// registry itself decides which peer maps to which interface.
func (l *CommLoop) registerDiagnosers() {
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	registry, ok := any(l.frame.CtxCommReg).(junction.ConnectionDiagnoserRegistry)
	if !ok {
		return
	}
	for _, rt := range l.ifaces {
		if rt == nil || rt.Impl == nil || rt.Name == "" {
			continue
		}
		if diagnosable, ok := rt.Impl.(connectionDiagnosable); ok {
			registry.RegisterConnectionDiagnoser(rt.Name, diagnosable.DiagnoseEndpoint)
		}
	}
}

func (l *CommLoop) unregisterDiagnosers() {
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}
	registry, ok := any(l.frame.CtxCommReg).(junction.ConnectionDiagnoserRegistry)
	if !ok {
		return
	}
	for _, rt := range l.ifaces {
		if rt != nil && rt.Name != "" {
			registry.UnregisterConnectionDiagnoser(rt.Name)
		}
	}
}
