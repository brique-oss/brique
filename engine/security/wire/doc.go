/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package wire verifies and builds the version-1 signed external envelope
// (SECURITY.md §6.1 to §6.3).
//
// The clear-mode builder takes already serialized payload bytes, owns a copy, hashes those
// exact bytes, canonicalizes the protected header, signs that exact representation through
// security/signer and embeds it literally in the envelope. Payload and envelope sizes are
// checked before a signature is requested. Because base64url expands data, the largest clear
// payload is slightly below the 3 MiB decoded bound while the envelope stays within 4 MiB;
// larger data belongs in Matter.
//
// Verification is strict and collapses every failure into one opaque error for the network
// peer; VerifyClearDiagnosed also returns a classified Diagnosis for the denial journal.
// Intentions return a stable signed-envelope hash (bmr1_) for the pending registry; responses
// require that request hash plus a canonical sequence and a running or final phase.
//
// An intention's header may carry actor_id ([a-z][a-z0-9._-]{0,63}): signed, so it cannot change
// in transit, but chosen by the sender, so the receiver uses it only to narrow access and to
// trace. A response that carries one is rejected, and a receiver that predates the field rejects
// a message that has it.
package wire
