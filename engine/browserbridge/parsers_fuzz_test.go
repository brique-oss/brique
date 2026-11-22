/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package browserbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzReadFrame(f *testing.F) {
	valid := func(payload []byte) []byte {
		var buffer bytes.Buffer
		_ = WriteFrame(&buffer, payload)
		return buffer.Bytes()
	}
	f.Add(valid([]byte(`{"format":"brique.browser.bridge/1","kind":"hello","sequence":1}`)))
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 'x'})
	f.Add([]byte{5, 0, 0, 0, 'a', 'b'})
	f.Fuzz(func(t *testing.T, input []byte) {
		payload, err := ReadFrame(bytes.NewReader(input))
		if err != nil {
			return
		}
		if len(payload) == 0 || len(payload) > MaxNativeFrameBytes {
			t.Fatalf("accepted a frame of %d bytes", len(payload))
		}
		if declared := int(binary.LittleEndian.Uint32(input[:4])); declared != len(payload) {
			t.Fatalf("frame declared %d bytes and returned %d", declared, len(payload))
		}
		var again bytes.Buffer
		if err := WriteFrame(&again, payload); err != nil || !bytes.Equal(again.Bytes(), input[:4+len(payload)]) {
			t.Fatalf("a read frame does not write back identically: %v", err)
		}
	})
}

func FuzzSessionHandle(f *testing.F) {
	f.Add([]byte(`{"format":"brique.browser.bridge/1","kind":"hello","sequence":2}`))
	f.Add([]byte(`{"format":"brique.browser.bridge/1","kind":"open","sequence":2,"session_id":"` + testSessionID + `","origin":"https://example.test","document_id":"d","frame_id":0,"ui_name":"root"}`))
	f.Add([]byte(`{"format":"brique.browser.bridge/1","kind":"message","sequence":2,"session_id":"` + testSessionID + `","channel_id":"` + testChannelID + `","payload_b64":"e30"}`))
	f.Add([]byte(`{"kind":"open","kind":"hello"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		sink := &recordingSink{}
		session := testSession(t, sink)
		if hello := session.Handle([]byte(`{"format":"brique.browser.bridge/1","kind":"hello","sequence":1}`)); !hello.OK {
			t.Fatalf("hello refused: %#v", hello)
		}
		response := session.Handle(raw)
		if response.Format != "" && response.Format != "brique.browser.bridge/1" {
			t.Fatalf("response format %q", response.Format)
		}
		if len(sink.opened) > 1 {
			t.Fatalf("one hostile frame opened %d channels", len(sink.opened))
		}
		for _, binding := range sink.opened {
			if binding.Origin == "" || binding.DocumentID == "" || binding.UIName == "" {
				t.Fatalf("a channel was opened without its attested coordinates: %#v", binding)
			}
		}
	})
}

func FuzzDecodeIPCHeader(f *testing.F) {
	f.Add([]byte(`{"format":"` + EngineIPCProtocol + `","kind":"open","sequence":1}`))
	f.Add([]byte(`{"format":"` + EngineIPCProtocol + `","kind":"open","kind":"close","sequence":1}`))
	f.Add([]byte(`{"format":"x","kind":"","sequence":0}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		header, err := decodeIPCHeader(raw)
		if err != nil {
			return
		}
		if header.Format != EngineIPCProtocol || header.Kind == "" || header.Sequence == 0 || len(raw) > MaxNativeFrameBytes {
			t.Fatalf("accepted an invalid header: %#v", header)
		}
	})
}
