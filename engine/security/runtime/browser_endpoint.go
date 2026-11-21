/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"brique_engine/networkpolicy"
	"brique_engine/security/instancedescriptor"
)

var ErrBrowserEndpoint = errors.New("browser endpoint refused")

type BrowserRoute struct {
	Authenticated *AuthenticatedInstanceDescriptor
	Endpoint      instancedescriptor.Endpoint
	Resolved      *networkpolicy.ResolvedEndpoint
}

// ResolveBrowserRoute is the only route from page-supplied descriptor bytes to
// a dialable browser endpoint. The signing key first selects one Brique Sec
// machine. The descriptor route must then stay under that machine's configured
// HTTPS target before DNS and every resolved address pass local network policy.
func (runtime *Runtime) ResolveBrowserRoute(ctx context.Context, descriptorBytes []byte, browserOrigin, endpointName string,
	at time.Time, policy *networkpolicy.Policy) (*BrowserRoute, error) {
	if ctx == nil || policy == nil {
		return nil, ErrBrowserEndpoint
	}
	authenticated, err := runtime.AuthenticateInstanceDescriptor(descriptorBytes, browserOrigin, at)
	if err != nil {
		return nil, ErrBrowserEndpoint
	}
	proposed, ok := selectDescriptorEndpoint(authenticated, endpointName)
	if !ok || !endpointUnderTarget(proposed.URL, authenticated.Endpoint.Target) {
		return nil, ErrBrowserEndpoint
	}
	resolved, err := policy.ResolveWSS(ctx, proposed.URL)
	if err != nil {
		return nil, ErrBrowserEndpoint
	}
	return &BrowserRoute{Authenticated: authenticated, Endpoint: proposed, Resolved: resolved}, nil
}

func selectDescriptorEndpoint(authenticated *AuthenticatedInstanceDescriptor, requested string) (instancedescriptor.Endpoint, bool) {
	if authenticated == nil || authenticated.Descriptor == nil {
		return instancedescriptor.Endpoint{}, false
	}
	endpoints := authenticated.Descriptor.Protected().Endpoints
	machine := authenticated.Endpoint.Name
	if machine == "" {
		if len(endpoints) != 1 || requested != "" && requested != endpoints[0].Name {
			return instancedescriptor.Endpoint{}, false
		}
		return endpoints[0], true
	}
	if requested != "" && requested != machine {
		return instancedescriptor.Endpoint{}, false
	}
	for _, endpoint := range endpoints {
		if endpoint.Name == machine {
			return endpoint, true
		}
	}
	return instancedescriptor.Endpoint{}, false
}

func endpointUnderTarget(proposed, configured string) bool {
	endpoint, endpointErr := url.Parse(proposed)
	target, targetErr := url.Parse(configured)
	if endpointErr != nil || targetErr != nil || target.Scheme != "https" ||
		(endpoint.Scheme != "wss" && endpoint.Scheme != "ws") || endpoint.Host != target.Host ||
		endpoint.User != nil || target.User != nil || endpoint.RawQuery != "" || target.RawQuery != "" ||
		endpoint.Fragment != "" || target.Fragment != "" || endpoint.RawPath != "" || target.RawPath != "" {
		return false
	}
	base := strings.TrimRight(target.Path, "/")
	if base == "" {
		base = "/"
	}
	if base == "/" {
		return strings.HasPrefix(endpoint.Path, "/")
	}
	return endpoint.Path == base || strings.HasPrefix(endpoint.Path, base+"/")
}
