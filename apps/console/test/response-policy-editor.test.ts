import test from "node:test";
import assert from "node:assert/strict";
import { APIError } from "../src/api.ts";
import {
  canEditResponsePolicy,
  examplePolicySample,
  previewPolicyBody,
  ResponsePolicyEditorController,
} from "../src/response-policy-editor.ts";
import type { PolicyPreview } from "../src/response-policy-editor.ts";
import type { Tool } from "../src/types.ts";

const tool: Tool = {
  id: "search-tool", workspace_id: "workspace", name: "search__web_12345678",
  description: "Search", risk: "read", input_schema: { type: "object" },
  http: { method: "", url: "", timeout_ms: 0 },
  mcp: { server_id: "search-server", tool_name: "web", schema_hash: "hash" },
  response_policy: { max_bytes: 65536 }, status: "published", enabled: true,
  version: 3, created_at: "2026-10-06T00:00:00Z",
};
const policy = { include: ["/results/*/title"], max_bytes: 65536 };
const updated: Tool = { ...tool, version: 4, response_policy: policy };
const preview: PolicyPreview = {
  tool_version: 3, original_bytes: 132, projected_bytes: 96,
  result: { content: [], structuredContent: { results: [{ title: "Example" }] } },
};
const api = (request: (path: string, options?: { method?: string; body?: unknown; signal?: AbortSignal }) => Promise<unknown>) => ({
  async request<T>(path: string, options?: { method?: string; body?: unknown; signal?: AbortSignal }): Promise<T> {
    return await request(path, options) as T;
  },
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("preview serializes a bounded sample envelope and refuses unsafe numeric identifiers", () => {
  assert.deepEqual(previewPolicyBody(3, policy, examplePolicySample), {
    expected_version: 3, response_policy: policy, sample: JSON.parse(examplePolicySample),
  });
  for (const sample of ["[]", "null", "invalid-json", '{"structuredContent":{"id":9007199254740993}}', '{"structuredContent":{"rows":[{"id":9223372036854775807}]}}', '{"value":1e999}']) {
    assert.throws(() => previewPolicyBody(3, policy, sample));
  }
  assert.throws(() => previewPolicyBody(3, policy, JSON.stringify({ content: [{ type: "text", text: "中".repeat(90000) }] })), /256 KiB/);
  // The body cap includes JSON escaping and policy/version metadata.
  assert.throws(() => previewPolicyBody(3, policy, JSON.stringify({ text: "\u0001".repeat(44000) })), /256 KiB/);
  assert.equal((previewPolicyBody(3, policy, '{"id":"9223372036854775807"}').sample).id, "9223372036854775807");
});

test("preview refuses decimal rounding and underflow but accepts exact decimal round trips", () => {
  for (const token of ["9007199254740991.1", "1e-400", "-1e-400", "1.0000000000000001", "0.1234567890123456789"]) {
    assert.throws(() => previewPolicyBody(3, policy, `{"structuredContent":{"value":${token}}}`), /rounded or underflow/, token);
  }
  for (const token of ["0.1", "1e-1", "0.1000", "1.25", "1.2500e2", "-0", "0e-400", "1.2345e-7"]) {
    assert.equal(previewPolicyBody(3, policy, `{"value":${token}}`).sample.value, JSON.parse(token), token);
  }
  const strings = JSON.stringify({ id: "9007199254740991.1", note: 'quoted \\" 1e-400', text: "1.0000000000000001" });
  assert.deepEqual(previewPolicyBody(3, policy, strings).sample, JSON.parse(strings));
});

test("policy editing requires an administrator and an MCP tool", async () => {
  let requests = 0;
  const client = api(async () => { requests++; return preview; });
  assert.equal(canEditResponsePolicy(true, tool), true);
  assert.equal(canEditResponsePolicy(false, tool), false);
  assert.equal(canEditResponsePolicy(true, { ...tool, mcp: undefined }), false);
  for (const [canManage, selected] of [[false, tool], [true, { ...tool, mcp: undefined }]] as const) {
    const controller = new ResponsePolicyEditorController(client, tool.id, canManage);
    controller.observe(selected);
    await controller.preview();
    assert.equal(await controller.save(), null);
  }
  assert.equal(requests, 0);
});

test("preview uses the pinned version and draft policy without saving or executing a tool", async () => {
  const calls: { path: string; body: unknown }[] = [];
  const controller = new ResponsePolicyEditorController(api(async (path, options) => {
    calls.push({ path, body: options?.body });
    return preview;
  }), tool.id, true);
  controller.observe(tool);
  controller.edit("include", "/results/*/title");
  await controller.preview();
  assert.deepEqual(calls, [{ path: `/tools/${tool.id}/response-policy/preview`, body: previewPolicyBody(3, policy, examplePolicySample) }]);
  assert.deepEqual(controller.getSnapshot().preview, preview);
  assert.equal(controller.getSnapshot().tool?.version, 3);
  assert.equal(controller.getSnapshot().dirty, true);
});

test("sample validation and preview failures retain exact inputs for correction and retry", async () => {
  let calls = 0;
  let reject = true;
  const controller = new ResponsePolicyEditorController(api(async () => {
    calls++;
    if (reject) throw new APIError("A selected field is missing", 400, "invalid");
    return preview;
  }), tool.id, true);
  controller.observe(tool);
  controller.edit("include", "/results/*/title");
  const unsafe = '{"structuredContent":{"id":9007199254740993}}';
  controller.edit("sample", unsafe);
  await controller.preview();
  assert.equal(calls, 0);
  assert.equal(controller.getSnapshot().sample, unsafe);
  assert.match(controller.getSnapshot().error, /exact numeric range/);
  controller.edit("sample", examplePolicySample);
  await controller.preview();
  assert.equal(controller.getSnapshot().sample, examplePolicySample);
  assert.equal(controller.getSnapshot().include, "/results/*/title");
  assert.equal(controller.getSnapshot().phase, "idle");
  assert.match(controller.getSnapshot().error, /selected field is missing/);
  reject = false;
  await controller.preview();
  assert.equal(calls, 2);
  assert.deepEqual(controller.getSnapshot().preview, preview);
});

test("changing an input invalidates an in-flight preview and ignores its late success or error", async () => {
  for (const rejects of [false, true]) {
    const pending = deferred<PolicyPreview>();
    let signal: AbortSignal | undefined;
    const controller = new ResponsePolicyEditorController(api(async (_path, options) => {
      signal = options?.signal;
      return pending.promise;
    }), tool.id, true);
    controller.observe(tool);
    const loading = controller.preview();
    controller.edit("include", "/results/*/url");
    if (rejects) pending.reject(new APIError("old failure", 400, "invalid"));
    else pending.resolve(preview);
    await loading;
    assert.equal(signal?.aborted, true);
    assert.equal(controller.getSnapshot().preview, null);
    assert.equal(controller.getSnapshot().error, "");
    assert.equal(controller.getSnapshot().include, "/results/*/url");
  }
});

test("save sends one version-bound request, returns the new tool, and accepts an unchanged-policy no-op", async () => {
  const pending = deferred<Tool>();
  const bodies: unknown[] = [];
  const controller = new ResponsePolicyEditorController(api(async (path, options) => {
    assert.equal(path, `/tools/${tool.id}/response-policy`);
    bodies.push(options?.body);
    return pending.promise;
  }), tool.id, true);
  controller.observe(tool);
  controller.edit("include", "/results/*/title");
  const saving = controller.save();
  assert.equal(await controller.save(), null);
  pending.resolve(updated);
  assert.equal(await saving, updated);
  assert.deepEqual(bodies, [{ expected_version: 3, response_policy: policy }]);
  assert.equal(controller.getSnapshot().tool?.version, 4);
  assert.equal(controller.getSnapshot().dirty, false);
  assert.match(controller.getSnapshot().notice, /already prepared operations keep/);
  // A response with the same current version represents the server's no-op result.
  assert.equal(await controller.save(), updated);
  assert.match(controller.getSnapshot().notice, /no version was added/);
});

test("409 retains the draft and sample, blocks automatic retry, and reload only rebases after explicit action", async () => {
  const calls: { path: string; body?: unknown }[] = [];
  let conflicted = false;
  const serverTool = { ...tool, version: 4, response_policy: { include: ["/results/*/url"], max_bytes: 4096 } };
  const controller = new ResponsePolicyEditorController(api(async (path, options) => {
    calls.push({ path, body: options?.body });
    if (path === `/tools/${tool.id}`) return serverTool;
    if (!conflicted) { conflicted = true; throw new APIError("version conflict", 409, "conflict"); }
    return { ...updated, version: 5 };
  }), tool.id, true);
  controller.observe(tool);
  controller.edit("include", "/results/*/title");
  const sample = examplePolicySample.replace("Example", "My sample");
  controller.edit("sample", sample);
  assert.equal(await controller.save(), null);
  assert.equal(controller.getSnapshot().needsReload, true);
  assert.match(controller.getSnapshot().error, /will not be retried automatically/);
  assert.equal(await controller.save(), null);
  await controller.preview();
  assert.equal(calls.length, 1);
  assert.equal(await controller.reload(), serverTool);
  assert.equal(calls.length, 2);
  assert.equal(controller.getSnapshot().include, "/results/*/title");
  assert.equal(controller.getSnapshot().sample, sample);
  assert.equal(controller.getSnapshot().tool?.version, 4);
  assert.equal(controller.getSnapshot().needsReload, false);
  assert.match(controller.getSnapshot().notice, /draft and sample are retained/);
  assert.equal((await controller.save())?.version, 5);
  assert.deepEqual(calls[2].body, { expected_version: 4, response_policy: policy });
});

test("ambiguous save responses require reload and cancelled editors cannot update a different selection", async () => {
  const uncertain = new ResponsePolicyEditorController(api(async () => { throw new TypeError("lost response"); }), tool.id, true);
  uncertain.observe(tool);
  assert.equal(await uncertain.save(), null);
  assert.equal(uncertain.getSnapshot().needsReload, true);
  assert.match(uncertain.getSnapshot().error, /update may have completed/);

  const pending = deferred<Tool>();
  const controller = new ResponsePolicyEditorController(api(async () => pending.promise), tool.id, true);
  controller.observe(tool);
  const saving = controller.save();
  controller.cancel(); // The selected tool changed or its detail panel was closed.
  pending.resolve(updated);
  assert.equal(await saving, null);
  assert.equal(controller.getSnapshot().tool?.version, 3);
  assert.equal(controller.getSnapshot().notice, "");
});

test("background version changes preserve edits and require explicit reload", () => {
  const controller = new ResponsePolicyEditorController(api(async () => tool), tool.id, true);
  controller.observe(tool);
  controller.edit("include", "/results/*/title");
  controller.observe({ ...updated, response_policy: { max_bytes: 4096 } });
  assert.equal(controller.getSnapshot().tool?.version, 3);
  assert.equal(controller.getSnapshot().include, "/results/*/title");
  assert.equal(controller.getSnapshot().needsReload, true);
  controller.observe({ ...updated, id: "other-tool" });
  assert.equal(controller.getSnapshot().tool?.id, tool.id);
});

test("revoked access disables further policy actions and clears a prior preview", async () => {
  let deny = false;
  let requests = 0;
  const controller = new ResponsePolicyEditorController(api(async () => {
    requests++;
    if (deny) throw new APIError("Forbidden", 403, "forbidden");
    return preview;
  }), tool.id, true);
  controller.observe(tool);
  await controller.preview();
  deny = true;
  await controller.preview();
  assert.equal(controller.getSnapshot().preview, null);
  assert.equal(controller.getSnapshot().denied, true);
  assert.equal(await controller.save(), null);
  assert.equal(controller.getSnapshot().denied, true);
  assert.equal(requests, 2);
});

test("artifact options survive observation and edits and are removed only by explicit disable", async () => {
  const artifact = { max_bytes: 262144, ttl_seconds: 120 };
  let current: Tool = { ...tool, response_policy: { ...policy, artifact } };
  const requests: unknown[] = [];
  const controller = new ResponsePolicyEditorController(api(async (_path, options) => {
    const body = options?.body as { response_policy: Tool["response_policy"] };
    requests.push(body);
    current = { ...current, version: current.version + 1, response_policy: body.response_policy };
    return current;
  }), tool.id, true);
  controller.observe(current);
  assert.equal(controller.getSnapshot().artifactEnabled, "true");
  assert.equal(controller.getSnapshot().artifactMaxBytes, "262144");
  assert.equal(controller.getSnapshot().artifactTTL, "120");
  controller.edit("include", "/results/*/url");
  await controller.save();
  assert.deepEqual(current.response_policy?.artifact, artifact);
  controller.edit("artifactMaxBytes", "524288");
  controller.edit("artifactTTL", "600");
  await controller.save();
  assert.deepEqual(current.response_policy?.artifact, { max_bytes: 524288, ttl_seconds: 600 });
  controller.edit("artifactTTL", "59");
  assert.equal(await controller.save(), null);
  assert.equal(requests.length, 2);
  controller.edit("artifactEnabled", "false");
  await controller.save();
  assert.equal(current.response_policy?.artifact, undefined);
  assert.equal(requests.length, 3);
});
