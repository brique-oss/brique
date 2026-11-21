/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestNativeFramesRoundTripWithoutDelimiterAmbiguity(t *testing.T) {
	first := []byte(`{"format":"brique.browser-native/1","operation":"hello"}`)
	second := []byte{0, '\n', 1, 2, 3}
	var stream bytes.Buffer
	if err := WriteFrame(&stream, first); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&stream, second); err != nil {
		t.Fatal(err)
	}
	for index, expected := range [][]byte{first, second} {
		actual, err := ReadFrame(&stream)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("frame %d = %q, %v", index, actual, err)
		}
	}
}

func TestNativeFramesRejectEmptyOversizedAndTruncatedInput(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{name: "empty", raw: []byte{0, 0, 0, 0}, want: ErrInvalidFrame},
		{name: "oversized", raw: header(MaxNativeFrameBytes + 1), want: ErrFrameTooLarge},
		{name: "truncated", raw: append(header(4), 'x'), want: ErrInvalidFrame},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadFrame(bytes.NewReader(test.raw)); !errors.Is(err, test.want) {
				t.Fatalf("ReadFrame error = %v, want %v", err, test.want)
			}
		})
	}
	if err := WriteFrame(&bytes.Buffer{}, make([]byte, MaxNativeFrameBytes+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("WriteFrame oversized error = %v", err)
	}
}

func TestNativeFrameWriterCompletesShortWrites(t *testing.T) {
	w := &oneByteWriter{}
	payload := []byte("bounded")
	if err := WriteFrame(w, payload); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadFrame(bytes.NewReader(w.data))
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatalf("short-write frame = %q, %v", decoded, err)
	}
}

type oneByteWriter struct{ data []byte }

func (writer *oneByteWriter) Write(value []byte) (int, error) {
	writer.data = append(writer.data, value[0])
	return 1, nil
}

func header(size int) []byte {
	value := make([]byte, 4)
	binary.LittleEndian.PutUint32(value, uint32(size))
	return value
}
