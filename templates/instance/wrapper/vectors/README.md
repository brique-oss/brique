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

# List parameters

`transit_lists.json` is the shared corpus of the list parameters (`Wrapper.md` sections 10.1 and 17.3): how a list is emitted
(inline, one object, one file or transient Matter, or element by element), how it is received, whether a target is local, how
several Matters under one name are grouped and how a list of Matter references is sent.

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_transit_list_vectors.py` |
| Go | `go/lib/wrapper/transit_list_vectors_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`listvec::run`; build with `-lcurl`) |

The inline limit is fixed at 64 KiB, so the cases use sizes far from the boundary. A case marked `unrepresentable_in` cannot be
written in that language. A runtime that disagrees is fixed to match this file, never the other way round without a protocol
decision.
