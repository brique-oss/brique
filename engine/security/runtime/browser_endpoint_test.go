/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package runtime

import "testing"

func TestBrowserEndpointMustRemainUnderConfiguredPeerTarget(t *testing.T) {
	for _, test := range []struct {
		name, proposed, configured string
		want                       bool
	}{
		{name: "child", proposed: "wss://peer.example/brique/ui", configured: "https://peer.example/brique", want: true},
		{name: "same", proposed: "wss://peer.example/brique", configured: "https://peer.example/brique", want: true},
		{name: "root", proposed: "wss://peer.example/anything", configured: "https://peer.example", want: true},
		{name: "other host", proposed: "wss://other.example/brique/ui", configured: "https://peer.example/brique"},
		{name: "other port", proposed: "wss://peer.example:8443/brique/ui", configured: "https://peer.example/brique"},
		{name: "prefix confusion", proposed: "wss://peer.example/brique-evil/ui", configured: "https://peer.example/brique"},
		{name: "query", proposed: "wss://peer.example/brique/ui?next=x", configured: "https://peer.example/brique"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := endpointUnderTarget(test.proposed, test.configured); got != test.want {
				t.Fatalf("endpointUnderTarget = %v, want %v", got, test.want)
			}
		})
	}
}
