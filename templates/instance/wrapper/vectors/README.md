# Protocol vectors

`wrapper_frames.json` is the shared corpus for the frame the engine and a wrapper
runtime exchange over the local WebSocket: either a raw message (no handle) or a
frame `{"brique_wrapper_frame":"brique.wrapper/1","message":{...},"delegation":"<43 chars>"}`.

Each runtime runs the same cases in its own test suite and must agree with the
`accept` column and, for accepted cases, with `message_kind` and `delegation`:

| Runtime | Test |
|---|---|
| engine (`junction`) | `junction/wrapper_frame_vectors_test.go` |
| Go | `go/lib/wrapper/frame_vectors_test.go` |
| Python | `python/lib/tests/test_frame_vectors.py` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`run_frame_vectors`) |

A new case is added here first, then every suite is run. A runtime that disagrees is
fixed to match this file, never the other way round without a protocol decision.
