/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0.
 */

package trust

import (
	"bytes"
	"testing"
)

func FuzzStrictJSONAndCanonicalization(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`null`),
		[]byte(`{}`),
		[]byte(`{"a":[true,false,null,"text",1]}`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"n":1.5}`),
		[]byte{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded any
		_ = StrictDecodeJSON(data, &decoded)

		canonical, err := CanonicalizeJSON(data)
		if err != nil {
			return
		}
		again, err := CanonicalizeJSON(canonical)
		if err != nil {
			t.Fatalf("canonical output was rejected: %v", err)
		}
		if !bytes.Equal(canonical, again) {
			t.Fatalf("canonicalization is not idempotent: first=%q second=%q", canonical, again)
		}
	})
}
