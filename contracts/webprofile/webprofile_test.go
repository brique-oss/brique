/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package webprofile

import "testing"

func TestValidateServerProfile(t *testing.T) {
	good := func() error {
		return ValidateServerProfile("127.0.0.1:8443", []string{"brique.example"}, []string{"https://brique.example"}, []string{"10.0.0.1"})
	}
	if err := good(); err != nil {
		t.Fatalf("coherent profile refused: %v", err)
	}
	for name, err := range map[string]error{
		"non loopback":     ValidateServerProfile("10.0.0.2:8443", []string{"brique.example"}, []string{"https://brique.example"}, []string{"10.0.0.1"}),
		"port zero":        ValidateServerProfile("127.0.0.1:0", []string{"brique.example"}, []string{"https://brique.example"}, []string{"10.0.0.1"}),
		"no host":          ValidateServerProfile("127.0.0.1:8443", nil, []string{"https://brique.example"}, []string{"10.0.0.1"}),
		"origin not https": ValidateServerProfile("127.0.0.1:8443", []string{"brique.example"}, []string{"http://brique.example"}, []string{"10.0.0.1"}),
		"origin not host":  ValidateServerProfile("127.0.0.1:8443", []string{"brique.example"}, []string{"https://other.example"}, []string{"10.0.0.1"}),
		"proxy not an IP":  ValidateServerProfile("127.0.0.1:8443", []string{"brique.example"}, []string{"https://brique.example"}, []string{"proxy"}),
	} {
		if err == nil {
			t.Errorf("%s: profile accepted", name)
		}
	}
	if err := ValidateLoopbackAddress("[::1]:9"); err != nil {
		t.Errorf("IPv6 loopback refused: %v", err)
	}
	if got := NormalizeAuthority(" Brique.Example. "); got != "brique.example" {
		t.Errorf("unexpected normalisation %q", got)
	}
}
