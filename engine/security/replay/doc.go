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

// Package replay is the durable replay ledger of signed external intentions
// (SECURITY.md §6.4).
//
// The uniqueness key is (audience_instance_id, principal_id, intention_id) and maps to one
// signed-request hash. Reserve returns new, duplicate or conflict, and never returns new
// before the journal record has crossed a durability barrier. The journal is append-only,
// made of length-prefixed records with a CRC32C; a torn final record is discarded (dispatch
// cannot have followed it), a checksum failure fails recovery closed, and on restart every
// recovered reserved, dispatched or running entry becomes outcome_unknown: it is never
// executed again automatically.
//
// The ledger bounds entries, entries per principal, journal bytes, cached responses and each
// cached envelope. Batcher provides bounded group commit: every accepted caller waits for the
// shared fsync, and a failed barrier poisons the ledger. Entries leave the ledger only through
// CompactDropping, which writes a complete checkpoint to a distinct empty file, syncs it, and
// calls an install callback (atomic rename and directory sync) while mutations are locked;
// Service.StartMaintenance runs it at startup and periodically, so the ledger does not fill up.
// See SECURITY.md for the measured costs.
package replay
