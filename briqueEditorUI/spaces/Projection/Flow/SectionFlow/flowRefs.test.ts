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

import { flowDataValue } from "../../../../Brique_Substrate/projection/Flow/flowData.js";
import { allStrings, slotStrings, summarize, wholeValue, withRows } from "./flowRefs.js";

test("a slot holds one reference or an ordered list of references", () => {
  assert.deepEqual(slotStrings(flowDataValue("/root/ctx/one")), ["/root/ctx/one"]);
  assert.deepEqual(slotStrings(flowDataValue(["/root/a/one", "$input.more", "/root/b/two"])), ["/root/a/one", "$input.more", "/root/b/two"]);
  assert.deepEqual(slotStrings(flowDataValue([])), []);
});

test("what is not a string is not a reference", () => {
  assert.deepEqual(slotStrings(flowDataValue(["/root/a/one", 7, null, { k: "/root/x" }])), ["/root/a/one"]);
  assert.deepEqual(slotStrings(flowDataValue({ k: "/root/x" })), []);
  assert.deepEqual(slotStrings(flowDataValue(3)), []);
  assert.deepEqual(slotStrings(undefined), []);
});

test("an output built from nested values is read through the strings it holds", () => {
  const messages = flowDataValue([{ to: "user:1", kind: "user", content: "$advice.payload.output.advice" }]);
  assert.deepEqual(allStrings(messages).filter((s) => s.startsWith("$")), ["$advice.payload.output.advice"]);
  assert.deepEqual(allStrings(flowDataValue({ a: ["x", { b: "$y.z" }], n: 1 })), ["x", "$y.z"]);
  assert.deepEqual(allStrings(flowDataValue(5)), []);
});

// a stand-in for detectBriqueRef (the renderer is JSX, which these tests do not load): runtime refs by their prefix
const detect = (_key: string, value: unknown) =>
  typeof value === "string" && value.startsWith("$")
    ? { kind: value.startsWith("$input") ? ("runtime-input" as const) : ("runtime-alias" as const), value }
    : null;

test("every parameter of a with block is shown under the name it takes in the invoked capacity", () => {
  const withBlock = flowDataValue({
    continuity_id: "$input.continuity_id",
    run_id: "$input.run_id",
    step: "advice",
    input: "$weather.payload",
    output_schema: { type: "object", properties: { advice: { type: "string" } }, required: ["advice"], additionalProperties: false },
    model: { provider: "recorded", model: "claude-haiku-4-5-20251001" },
    "@matter": { prompt_matters: ["/root/agentic/pilot/weather_advice_prompt"] },
  });
  assert.ok(typeof withBlock === "object" && withBlock !== null && withBlock.kind === "object");
  const rows = withRows(withBlock.entries, detect);
  assert.deepEqual(rows.map((r) => r.name), ["continuity_id", "run_id", "step", "input", "output_schema", "model", "prompt_matters"]);
  assert.deepEqual(rows[0].refs, [{ kind: "runtime-input", value: "$input.continuity_id" }]);
  assert.deepEqual(rows[3].refs, [{ kind: "runtime-alias", value: "$weather.payload" }]);
  assert.equal(rows[2].literal, '"advice"');                                  // a literal is shown, not hidden
  assert.equal(rows[5].literal, '{"provider":"recorded","model":"claude-haiku-4-5-20251001"}');   // a model says which one
  assert.ok(rows[4].literal?.startsWith('{"type":"object","properties":{"advice"'));              // a schema starts to say what it is
  assert.ok(rows[4].literal!.length <= 64 && rows[4].literal!.endsWith("…"));                     // and is cut short
  assert.deepEqual(JSON.parse(rows[4].full!), { type: "object", properties: { advice: { type: "string" } }, required: ["advice"], additionalProperties: false });   // the whole of it is in the tooltip
  assert.deepEqual(rows[6].refs, [{ kind: "matter", value: "/root/agentic/pilot/weather_advice_prompt" }]);   // under the slot's own name
});

test("several Matters under one name are one row, in order; a runtime list under a slot is a reference too", () => {
  const withBlock = flowDataValue({
    "@matter": { prompts: ["/root/a/one", "/root/b/two"], more: "$input.message.content.matters" },
    parts: ["x", "$input.texts"],
  });
  assert.ok(typeof withBlock === "object" && withBlock !== null && withBlock.kind === "object");
  const rows = withRows(withBlock.entries, detect);
  assert.deepEqual(rows[0], { name: "prompts", refs: [{ kind: "matter", value: "/root/a/one" }, { kind: "matter", value: "/root/b/two" }] });
  assert.deepEqual(rows[1], { name: "more", refs: [{ kind: "runtime-input", value: "$input.message.content.matters" }] });
  assert.deepEqual(rows[2].refs, [{ kind: "runtime-input", value: "$input.texts" }]);                       // a list built from a runtime ref
});

test("a value is summarized by its compact JSON, cut short, and kept whole for the tooltip", () => {
  assert.equal(summarize(flowDataValue("short")), '"short"');
  assert.equal(summarize(flowDataValue("x".repeat(100))).length, 64);
  assert.ok(summarize(flowDataValue("x".repeat(100))).endsWith("…"));
  assert.equal(summarize(flowDataValue(7)), "7");
  assert.equal(summarize(flowDataValue(null)), "null");
  assert.equal(summarize(flowDataValue([1, 2, 3])), "[1,2,3]");
  assert.equal(summarize(flowDataValue({ a: 1 })), '{"a":1}');
  assert.equal(summarize(undefined), "null");
  assert.equal(wholeValue(flowDataValue({ a: [1, 2] })), '{\n  "a": [\n    1,\n    2\n  ]\n}');
});
