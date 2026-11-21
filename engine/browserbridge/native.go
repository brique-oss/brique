/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package browserbridge contains the browser-facing native-messaging framing.
// It has no Brique authority: the engine remains the sole admission boundary.
package browserbridge

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	NativeProtocol      = "brique.browser-native/1"
	MaxNativeFrameBytes = 256 << 10
)

var (
	ErrInvalidFrame  = errors.New("invalid browser native-messaging frame")
	ErrFrameTooLarge = errors.New("browser native-messaging frame exceeds limit")
)

// ReadFrame reads Chrome/Firefox native-messaging's uint32 little-endian
// length followed by exactly one payload. Limits are applied before allocation.
func ReadFrame(source io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 {
		return nil, ErrInvalidFrame
	}
	if size > MaxNativeFrameBytes {
		return nil, ErrFrameTooLarge
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(source, payload); err != nil {
		return nil, ErrInvalidFrame
	}
	return payload, nil
}

// WriteFrame writes one bounded native-messaging frame.
func WriteFrame(destination io.Writer, payload []byte) error {
	if len(payload) == 0 {
		return ErrInvalidFrame
	}
	if len(payload) > MaxNativeFrameBytes {
		return ErrFrameTooLarge
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(destination, header[:]); err != nil {
		return err
	}
	return writeAll(destination, payload)
}

func writeAll(destination io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := destination.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(content) {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}
