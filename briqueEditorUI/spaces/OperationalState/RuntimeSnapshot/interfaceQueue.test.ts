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

import assert from "node:assert/strict";
import { test } from "node:test";
import type { InterfaceQueueSnapshot } from "../../../Brique_Substrate/capability/typed.js";
import { formatMs, interfaceQueueTone } from "./interfaceQueue.js";

const base: InterfaceQueueSnapshot = {
  name: "front_py", type: "wrapper", status: "running", queue_places: 16, queue_length: 0,
  offers: 100, found_full: 0, given_up: 0, waited_ms: 0, longest_wait_ms: 0,
};

test("an interface that never met a full queue is normal", () => {
  assert.equal(interfaceQueueTone(base), "normal");
});

test("a queue found full is a warning, a consumer given up is an error", () => {
  assert.equal(interfaceQueueTone({ ...base, found_full: 3 }), "warning");
  assert.equal(interfaceQueueTone({ ...base, found_full: 3, given_up: 1 }), "error");
  assert.equal(interfaceQueueTone({ ...base, given_up: 1 }), "error");
});

test("waits are shown at the precision that matters", () => {
  assert.equal(formatMs(0.25), "0.25 ms");
  assert.equal(formatMs(2.5), "2.5 ms");
  assert.equal(formatMs(1250), "1250 ms");
});
