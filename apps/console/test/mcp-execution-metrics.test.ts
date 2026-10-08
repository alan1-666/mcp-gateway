import test from "node:test";
import assert from "node:assert/strict";
import {
  executionHealthLabel,
  executionPhaseLabels,
} from "../src/mcp-execution-metrics";
import { mcpZh } from "../src/locale-mcp";

test("Observed execution labels do not imply availability and have Chinese copy", () => {
  for (const health of [
    "healthy",
    "degraded",
    "attention",
    "stale",
    "disabled",
    "unobserved",
    "future-status",
  ]) {
    const label = executionHealthLabel(health);
    assert.ok(mcpZh[label], label);
    assert.notEqual(label, "Healthy");
  }
  assert.equal(
    executionHealthLabel("future-status"),
    "Observation unavailable",
  );
  assert.equal(Object.keys(executionPhaseLabels).length, 6);
  for (const label of Object.values(executionPhaseLabels))
    assert.ok(mcpZh[label]);
});
