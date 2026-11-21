/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package webhost serves an immutable Spark web publication. It contains no
// Brique identity, admission, Intention routing or UI build logic.
package webprofile

import (
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
)

// ValidateLoopbackAddress reports whether address is an explicit loopback host:port.
func ValidateLoopbackAddress(address string) error { return RequireLoopbackAddress(address) }

// ValidateServerProfile applies, without binding anything, the exact checks Start
// applies to a server profile: a fixed loopback address, exact public authorities,
// exact HTTPS origins matching those authorities and exact reverse-proxy IPs.
// The launcher uses it so that an incoherent profile is refused when recorded,
// not only when the web host starts.
func ValidateServerProfile(address string, allowedHosts, publicOrigins, trustedProxies []string) error {
	if err := RequireLoopbackAddress(address); err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(address)
	if portNumber, err := net.LookupPort("tcp", port); err != nil || portNumber == 0 {
		return errors.New("server profile requires a fixed web host port")
	}
	if len(allowedHosts) == 0 || len(publicOrigins) == 0 || len(trustedProxies) == 0 {
		return errors.New("server profile requires an allowed host, a public origin and a trusted proxy")
	}
	authorities, err := AllowedAuthorities(address, allowedHosts)
	if err != nil {
		return err
	}
	if _, err := ExactIPSet(trustedProxies); err != nil {
		return err
	}
	_, _, err = AllowedPublicOrigins(address, publicOrigins, authorities)
	return err
}

func RequireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("web host address must include host and port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("web host currently requires an explicit loopback address")
	}
	return nil
}

func AllowedAuthorities(bound string, configured []string) (map[string]bool, error) {
	result := map[string]bool{NormalizeAuthority(bound): true}
	for _, value := range configured {
		normalized := NormalizeAuthority(value)
		if normalized == "" || strings.ContainsAny(value, "/\\@") {
			return nil, errors.New("invalid allowed web host authority")
		}
		result[normalized] = true
	}
	return result, nil
}

func NormalizeAuthority(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		if strings.ContainsAny(value, "[]:") || value == "" {
			return ""
		}
		host = value
		port = ""
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

func ExactIPSet(values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || result[ip.String()] {
			return nil, errors.New("invalid or duplicate trusted proxy address")
		}
		result[ip.String()] = true
	}
	return result, nil
}

func AllowedPublicOrigins(bound string, configured []string, authorities map[string]bool) (map[string]bool, []string, error) {
	origins := make(map[string]bool, len(configured)+1)
	webSockets := make([]string, 0, len(configured))
	for _, value := range configured {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || value != "https://"+parsed.Host {
			return nil, nil, errors.New("invalid public web origin")
		}
		authority := NormalizeAuthority(parsed.Host)
		if authority == "" || !authorities[authority] || origins[value] {
			return nil, nil, errors.New("public web origin does not match an allowed host")
		}
		origins[value] = true
		webSockets = append(webSockets, "wss://"+parsed.Host)
	}
	sort.Strings(webSockets)
	return origins, webSockets, nil
}
