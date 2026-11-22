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

# Busy retry

`busy_retry.json` fixes how a call refused because its callee is busy is asked again: which responses count as busy
(`details.reason` is `busy` or `loop busy`), the bounds of the pause before each new attempt (base doubled, capped, times a random
factor from 0.5 to 1.5) and how the attempt setting (`BRIQUE_BUSY_ATTEMPTS`) ignores an invalid value. A timeout is never retried.

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_busy_retry.py` |
| Go | `go/lib/wrapper/busy_retry_test.go`, `raw_retry_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`busyvec::run`) |

# Typed errors

`errors.json` fixes how the error object of an engine response is classified: the kind (`busy`, `revision_mismatch`,
`line_count_mismatch`, `connection_lost`, `not_found`, `other`), whether the outcome is unknown, and the defaults of a missing code or message.

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_errors.py` |
| Go | `go/lib/wrapper/call_error_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`run_error_kinds`) |

# Settings

`settings.json` is the single source of the numeric settings of the runtime: name, default, minimum, unit, policy (`strict` stops the
wrapper at start, `tolerant` ignores an invalid value) and what each one sets. Each runtime declares the same settings and its test compares
them to this file; the table of Wrapper.md §14.1.3 is generated from it (`python3 -m wrapper.settings table settings.json`, from `python/lib`).

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_settings.py` |
| Go | `go/lib/wrapper/settings_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`run_settings`) |

# Matter client

`matter_client.json` fixes how a `matter.read` payload is understood (inline text or lease URL, malformed lease, no substance), how a revision
is read, how journal lines are parsed and written (compact JSON, one per line, LF-ended, no escaping of `<`, `>` or `&`), the inline limit of an
append (240 KiB), and the pause between two attempts of a guarded write. A case marked `unrepresentable_in` cannot be written in that language.

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_matter_client.py` |
| Go | `go/lib/wrapper/matter_client_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`mattervec::run`) |

# Running response

`running_response.json` fixes the message a capacity sends to say it is still working: it answers the intention being served, addressed to the
caller, from the capacity, with no payload; the identity is the wrapper's except in a delegated call. An empty identity or payload may be absent
from the wire (the Go runtime omits them): the comparison treats absent and empty as equal.

| Runtime | Test |
|---|---|
| Python | `python/lib/tests/test_keepalive.py` |
| Go | `go/lib/wrapper/running_test.go` |
| C++ | `cpp/lib/tests/runtime_test.cpp` (`run_running`) |
