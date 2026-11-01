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

// Package pending is the process-local registry of awaited OUTBOUND external
// intentions (SECURITY.md §6.5). A record is installed before the signed request can
// leave the process and consulted before an authenticated response is released to its
// local origin.
//
// Each entry binds the intention id, the exact bmr1_ request hash, the expected
// principal, the originating local address, the minimum accepted content mode, a
// refreshable silence timeout, an absolute deadline and the last committed response
// sequence. Only a verified wire response can begin delivery, and principal, id, request
// hash, content mode, sequence, phase and inner status must all agree. Sequence gaps are
// allowed; a duplicate or older sequence is rejected and does not refresh the silence
// deadline.
//
// Delivery is two-phase: BeginResponse reserves one unforgeable permit, Commit advances a
// running sequence or consumes a final entry once the local destination accepted it, and
// Abort releases the permit so the same signed response stays retryable. The registry is
// bounded (4096 entries by default). Expire reports silence and deadline notifications,
// and DrainStateLost reports response_state_lost on shutdown, which never implies that
// retrying the remote effect is safe.
//
// It has the opposite direction from the inbound reply route kept by communication: that
// remembers where the answer to an inbound external intention must go; this authenticates
// and correlates the answers to outbound ones.
package pending
