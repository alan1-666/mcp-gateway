import assert from "node:assert/strict";
import test from "node:test";
import { sessionContractLabel } from "../src/mcp-compatibility";
import { translate, chineseCopy } from "../src/console-language";
import { readFileSync } from "node:fs";

test("fresh-session labels distinguish observations, changed contracts and missing verification", () => {
  for (const status of [undefined, "not_checked", "", "__proto__", "unknown"]) {
    assert.equal(sessionContractLabel(status), "Not checked");
  }
  for (const status of ["stable", "changed", "unverified"]) {
    const label = sessionContractLabel(status);
    assert.notEqual(label, "Not checked");
    assert.equal(translate("en", label), label);
    assert.notEqual(translate("zh", label), label);
  }
  assert.equal(translate("zh", sessionContractLabel()), "未检查");
});

test("new gateway-owned diagnostic messages have Chinese translations", () => {
  const source = readFileSync(
    new URL(
      "../../../internal/adapters/mcpadapter/session_compatibility.go",
      import.meta.url,
    ),
    "utf8",
  );
  const messages = [...source.matchAll(/"([A-Z][^"\n]*[.;][^"\n]*)"/g)].map(
    (match) => match[1],
  );
  assert.ok(messages.length >= 8);
  for (const message of messages) {
    assert.ok(Object.hasOwn(chineseCopy, message), message);
  }
});
