/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package networkpolicy resolves browser transport endpoints once, checks
// every resulting address against a local policy and provides a dial function
// pinned to those checked addresses. A signed URL is never an authorization to
// reach the local network.
package networkpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

var ErrDestinationDenied = errors.New("network destination denied")

var specialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Options struct {
	AllowPublicInternet    bool
	AllowCleartextLoopback bool
	AllowedPrefixes        []netip.Prefix
	MaxAddresses           int
	Resolver               Resolver
	Dialer                 *net.Dialer
}

type Policy struct {
	allowPublic   bool
	allowLoopback bool
	allowed       []netip.Prefix
	maxAddresses  int
	resolver      Resolver
	dialer        *net.Dialer
}

type ResolvedEndpoint struct {
	url           string
	authority     string
	dialAuthority string
	addresses     []netip.Addr
	dialer        *net.Dialer
}

func New(options Options) (*Policy, error) {
	if options.MaxAddresses == 0 {
		options.MaxAddresses = 8
	}
	if options.MaxAddresses < 1 || options.MaxAddresses > 64 {
		return nil, ErrDestinationDenied
	}
	allowed := make([]netip.Prefix, len(options.AllowedPrefixes))
	seen := make(map[netip.Prefix]struct{}, len(allowed))
	for index, prefix := range options.AllowedPrefixes {
		if !prefix.IsValid() || prefix.Addr().Is4In6() {
			return nil, ErrDestinationDenied
		}
		prefix = prefix.Masked()
		if _, duplicate := seen[prefix]; duplicate {
			return nil, ErrDestinationDenied
		}
		seen[prefix] = struct{}{}
		allowed[index] = prefix
	}
	sort.Slice(allowed, func(i, j int) bool {
		if allowed[i].Addr().Compare(allowed[j].Addr()) == 0 {
			return allowed[i].Bits() < allowed[j].Bits()
		}
		return allowed[i].Addr().Compare(allowed[j].Addr()) < 0
	})
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	} else {
		copy := *dialer
		dialer = &copy
	}
	return &Policy{allowPublic: options.AllowPublicInternet, allowLoopback: options.AllowCleartextLoopback,
		allowed: allowed, maxAddresses: options.MaxAddresses, resolver: resolver, dialer: dialer}, nil
}

func (policy *Policy) ResolveWSS(ctx context.Context, rawURL string) (*ResolvedEndpoint, error) {
	if policy == nil || ctx == nil {
		return nil, ErrDestinationDenied
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.String() != rawURL || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.RawPath != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return nil, ErrDestinationDenied
	}
	host := parsed.Hostname()
	if host != strings.ToLower(host) || host != strings.TrimSuffix(host, ".") || !ascii(host) || strings.ContainsAny(host, " %") {
		return nil, ErrDestinationDenied
	}
	port := parsed.Port()
	switch parsed.Scheme {
	case "wss":
		if port == "443" {
			return nil, ErrDestinationDenied
		}
		if port == "" {
			port = "443"
		}
	case "ws":
		if !policy.allowLoopback || port == "80" {
			return nil, ErrDestinationDenied
		}
		if port == "" {
			port = "80"
		}
	default:
		return nil, ErrDestinationDenied
	}
	if parsed.Port() != "" && parsed.Host != net.JoinHostPort(host, parsed.Port()) {
		return nil, ErrDestinationDenied
	}
	addresses, err := policy.lookup(ctx, host)
	if err != nil || len(addresses) == 0 || len(addresses) > policy.maxAddresses {
		return nil, ErrDestinationDenied
	}
	for _, address := range addresses {
		if parsed.Scheme == "ws" {
			if !address.IsLoopback() {
				return nil, ErrDestinationDenied
			}
			continue
		}
		if !policy.allowedAddress(address) {
			return nil, ErrDestinationDenied
		}
	}
	return &ResolvedEndpoint{url: rawURL, authority: parsed.Host, dialAuthority: net.JoinHostPort(host, port),
		addresses: append([]netip.Addr(nil), addresses...), dialer: policy.dialer}, nil
}

func (policy *Policy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{canonicalAddr(literal)}, nil
	}
	addresses, err := policy.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = canonicalAddr(address)
		if !address.IsValid() || address.Zone() != "" {
			return nil, ErrDestinationDenied
		}
		if _, exists := unique[address]; exists {
			continue
		}
		unique[address] = struct{}{}
		result = append(result, address)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Compare(result[j]) < 0 })
	return result, nil
}

func (policy *Policy) allowedAddress(address netip.Addr) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range policy.allowed {
		if prefix.Contains(address) {
			return true
		}
	}
	return policy.allowPublic && publicAddress(address)
}

func publicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func canonicalAddr(address netip.Addr) netip.Addr {
	if address.Is4In6() {
		return address.Unmap()
	}
	return address
}

func (endpoint *ResolvedEndpoint) URL() string {
	if endpoint == nil {
		return ""
	}
	return endpoint.url
}
func (endpoint *ResolvedEndpoint) Authority() string {
	if endpoint == nil {
		return ""
	}
	return endpoint.authority
}
func (endpoint *ResolvedEndpoint) Addresses() []netip.Addr {
	if endpoint == nil {
		return nil
	}
	return append([]netip.Addr(nil), endpoint.addresses...)
}

// DialContext is suitable for websocket.Dialer.NetDialContext. It rejects any
// authority other than the one resolved above and dials IP literals directly,
// preventing a second DNS lookup between policy evaluation and connection.
func (endpoint *ResolvedEndpoint) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if endpoint == nil || endpoint.dialer == nil || address != endpoint.dialAuthority ||
		(network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, ErrDestinationDenied
	}
	var last error
	_, port, _ := net.SplitHostPort(endpoint.dialAuthority)
	for _, candidate := range endpoint.addresses {
		if network == "tcp4" && !candidate.Is4() || network == "tcp6" && candidate.Is4() {
			continue
		}
		connection, err := endpoint.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return connection, nil
		}
		last = err
	}
	if last == nil {
		last = ErrDestinationDenied
	}
	return nil, last
}

func ascii(value string) bool {
	for index := range len(value) {
		if value[index] > 0x7f {
			return false
		}
	}
	return true
}
