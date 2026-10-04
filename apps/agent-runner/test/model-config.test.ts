import assert from "node:assert/strict";
import { test } from "node:test";
import { resolveModelSelection } from "../src/model-config.js";

test("blank model environment falls back to persisted choices; explicit overrides retain precedence", () => {
  const defaults = { provider: "openai", modelId: "gpt-5.5" };
  const saved = { provider: "subscription-provider", modelId: "session-model" };
  assert.deepEqual(resolveModelSelection({ PI_PROVIDER: "", PI_MODEL: " \t" }, undefined, defaults), defaults);
  assert.deepEqual(resolveModelSelection({}, undefined, defaults), defaults);
  assert.deepEqual(resolveModelSelection({ PI_PROVIDER: " ", PI_MODEL: "" }, saved, defaults), saved);
  assert.deepEqual(resolveModelSelection({ PI_PROVIDER: " openai ", PI_MODEL: " gpt-5.5 " }, saved, {}), defaults);
  assert.deepEqual(resolveModelSelection({ PI_PROVIDER: "", PI_MODEL: "" }, null, {}), { provider: undefined, modelId: undefined });
});
