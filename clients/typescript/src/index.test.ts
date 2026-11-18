import { test } from "node:test";
import assert from "node:assert/strict";
import { parseEvent } from "./index.js";

test("parseEvent reads event and data", () => {
  assert.deepEqual(parseEvent('event: session\ndata: {"a":1}'), { event: "session", data: '{"a":1}' });
});

test("parseEvent skips comments", () => {
  assert.equal(parseEvent(": ping"), undefined);
});
