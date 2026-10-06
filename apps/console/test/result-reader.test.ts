import assert from "node:assert/strict";
import test from "node:test";
import { appendResultPage } from "../src/result-reader";
import type { ResultPage } from "../src/result-reader";
const base: ResultPage = {
  operation_id: "op",
  bytes: 7,
  sha256: "a".repeat(64),
  expires_at: "2030-01-01T00:00:00Z",
  format: "json_utf8",
  offset: 0,
  chunk: '{"a":1}',
};
test("ordered JSON chunks preserve unicode and exact large integers", () => {
  assert.equal(appendResultPage("op", "", "", base), '{"a":1}');
  const raw = '{"中文":9007199254740993}';
  const length = new TextEncoder().encode(raw).length;
  const first = '{"中文":';
  const accumulated = appendResultPage("op", "", "", {
    ...base,
    bytes: length,
    chunk: first,
    next_cursor: "next",
  });
  assert.equal(
    appendResultPage("op", accumulated, base.sha256, {
      ...base,
      bytes: length,
      offset: new TextEncoder().encode(first).length,
      chunk: "9007199254740993}",
      next_cursor: "",
    }),
    raw,
  );
});
test("cross-operation, changed, oversized and malformed result chunks fail closed", () => {
  const bad: unknown[] = [
    null,
    { ...base, operation_id: "another" },
    { ...base, format: "other" },
    { ...base, bytes: 0 },
    { ...base, bytes: 1.2 },
    { ...base, bytes: 1048577 },
    { ...base, sha256: "bad" },
    { ...base, expires_at: "bad" },
    { ...base, offset: 1 },
    { ...base, chunk: 1 },
    { ...base, chunk: "" },
    { ...base, chunk: "a".repeat(16385) },
    { ...base, next_cursor: 5 },
    { ...base, next_cursor: "a".repeat(513) },
  ];
  for (const page of bad)
    assert.throws(
      () => appendResultPage("op", "", "", page as ResultPage),
      /invalid or changed/,
    );
  assert.throws(
    () => appendResultPage("op", "", "b".repeat(64), base),
    /invalid or changed/,
  );
  assert.throws(
    () => appendResultPage("op", "", "", { ...base, bytes: 6 }),
    /inconsistent/,
  );
  assert.throws(
    () => appendResultPage("op", "", "", { ...base, bytes: 8 }),
    /inconsistent/,
  );
  assert.throws(
    () => appendResultPage("op", "", "", { ...base, next_cursor: "extra" }),
    /inconsistent/,
  );
});
