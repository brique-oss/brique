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
import test from "node:test";

import { POPOVER_OFFSET, popoverPlace } from "./hoverPosition.js";

const viewport = { width: 1000, height: 800 };

test("in the top-left of the window the popover opens to the right of and below the pointer", () => {
  assert.deepEqual(popoverPlace({ x: 100, y: 100 }, viewport), { left: 100 + POPOVER_OFFSET, top: 100 + POPOVER_OFFSET });
});

test("in the right half it is anchored by its right edge, so it opens to the left of the pointer", () => {
  assert.deepEqual(popoverPlace({ x: 900, y: 100 }, viewport), { right: 1000 - 900 + POPOVER_OFFSET, top: 100 + POPOVER_OFFSET });
});

test("in the bottom half it is anchored by its bottom edge, so it opens above the pointer", () => {
  assert.deepEqual(popoverPlace({ x: 100, y: 700 }, viewport), { left: 100 + POPOVER_OFFSET, bottom: 800 - 700 + POPOVER_OFFSET });
  assert.deepEqual(popoverPlace({ x: 900, y: 700 }, viewport), { right: 1000 - 900 + POPOVER_OFFSET, bottom: 800 - 700 + POPOVER_OFFSET });
});

test("exactly in the middle it opens to the right and below: it never gets both sides", () => {
  const place = popoverPlace({ x: 500, y: 400 }, viewport);
  assert.deepEqual(Object.keys(place).sort(), ["left", "top"]);
});
