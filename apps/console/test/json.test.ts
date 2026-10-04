import assert from "node:assert/strict";
import test from "node:test";
import { hasUnsafeNumbers, parseObject } from "../src/json.ts";

test("rejects large integer arguments before they can be rounded into a request", () => {
  for (const value of ["9007199254740993", "-9007199254740993", "1e400"]) {
    assert.throws(
      () => parseObject(`{"customer_id":${value}}`, "Arguments"),
      /Use a string/,
    );
  }
  assert.throws(
    () => parseObject('{"items":[{"id":9007199254740993}]}', "Arguments"),
    /exact numeric range/,
  );
});

test("preserves safe numbers and string identifiers", () => {
  assert.deepEqual(
    parseObject(
      '{"id":"9007199254740993","limit":9007199254740991,"ratio":0.25}',
      "Arguments",
    ),
    {
      id: "9007199254740993",
      limit: Number.MAX_SAFE_INTEGER,
      ratio: 0.25,
    },
  );
  assert.equal(
    hasUnsafeNumbers({
      nested: [null, true, "9007199254740993", { count: 42 }],
    }),
    false,
  );
});

test("detects unsafe integers in returned operation arguments for approval blocking", () => {
  assert.equal(
    hasUnsafeNumbers(JSON.parse('{"records":[{"account":9007199254740993}]}')),
    true,
  );
  assert.equal(hasUnsafeNumbers({ value: Infinity }), true);
  assert.equal(hasUnsafeNumbers({ value: NaN }), true);
});

test("rejects invalid JSON and non-object arguments", () => {
  assert.throws(() => parseObject("{broken}", "Arguments"), /valid JSON/);
  for (const value of ["null", "[]", '"text"', "42"]) {
    assert.throws(() => parseObject(value, "Arguments"), /JSON object/);
  }
});
