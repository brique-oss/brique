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

export const KeyKind = "kind";
export const KeyTS = "ts";
export const KeyIntention = "intention";
export const KeyResponse = "response";

export const ValueKindIntention = "intention";
export const ValueKindResponse = "response";

// Must stay aligned with engine/shared.DefaultControlMessageBytes. Larger
// business payloads travel through Matter, never through an intention.
export const CONTROL_MESSAGE_LIMIT_BYTES = 4 * 1024 * 1024;

export type MessageKind = typeof ValueKindIntention | typeof ValueKindResponse;
