// Driven by TestCloudWorkerIntegration with an isolated PostgreSQL schema.
// No model account, provider request or company endpoint is used.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
import { CloudWorker } from '../apps/agent-runner/src/worker.ts';
import { WorkerClient } from '../apps/agent-runner/src/worker-client.ts';
import { GatewayClient } from '../apps/agent-runner/src/client.ts';
import { createGatewayTools } from '../apps/agent-runner/src/tools.ts';
import { IntentJournal } from '../apps/agent-runner/src/journal.ts';

let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const api = new WorkerClient(config.baseURL, config.secret);
let ready = true;
let execute = async () => { throw new Error('unexpected inference'); };
const worker = new CloudWorker({ api, stateDir: config.stateDir, workerId: 'integration-worker',
  heartbeatMs: 50,
  probeModel: async () => ready ? { ready: true, provider: 'test', modelId: 'deterministic' } : { ready: false, errorCode: 'MODEL_NOT_CONFIGURED' },
  execute: ctx => execute(ctx),
});
async function call(path, body, token = config.token, want = 200) {
  const response = await fetch(`${config.baseURL}/api/v1${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  assert.equal(response.status, want, `${path}: unexpected HTTP status`);
  return response.json();
}
async function create(prompt) {
  const intent = { prompt, idempotency_key: randomUUID() };
  const run = await call('/runs', intent);
  assert.equal((await call('/runs', intent)).id, run.id, 'public retry identity');
  return run;
}
const runPath = run => `/runs/${run.id}`;

// Actual Go auth, queue, worker client, operation binding, HTTP adapter and events.
execute = async ctx => {
  ctx.emit('MODEL_STARTED', { provider: 'test' });
  const local = new GatewayClient(config.baseURL, config.token);
  // Max-sized escaped descriptions still fit the production clients' 256 KiB limit.
  const boundedLocal = await local.search('filler_', undefined, { limit: 50 });
  const boundedCloud = await ctx.gateway.search('filler_', undefined, { limit: 50 });
  assert.deepEqual(boundedLocal.items, boundedCloud.items);
  assert.equal(boundedCloud.items.length, 50); assert.equal(boundedCloud.total, 510);
  assert.ok(boundedCloud.items.every(tool => Buffer.byteLength(tool.description, 'utf8') <= 512));
  const localFirst = await local.search('integration_', undefined, { limit: 1 });
  assert.equal(localFirst.total, 2); assert.equal(localFirst.items.length, 1);
  assert.ok(localFirst.next_cursor);
  const localLast = await local.search('integration_', undefined, { limit: 1, cursor: localFirst.next_cursor });
  assert.equal(localLast.total, 2); assert.equal(localLast.items.length, 1); assert.ok(!localLast.next_cursor);
  const journal = new IntentJournal(join(ctx.store.directory, 'discovery-intents'), ctx.run.id, 'discovery-session');
  try {
    const tools = createGatewayTools(ctx.gateway, journal);
    const invoke = async (name, args) => {
      const result = await tools.find(tool => tool.name === name).execute('discovery', args, ctx.signal, undefined, {});
      assert.equal(result.content[0].type, 'text');
      return JSON.parse(result.content[0].text);
    };
    const first = await invoke('search_tools', { query: 'integration_', limit: 1 });
    assert.deepEqual(first.items, localFirst.items); assert.equal(first.total, 2); assert.ok(first.next_cursor);
    const last = await invoke('search_tools', { query: 'integration_', limit: 1, cursor: first.next_cursor });
    assert.deepEqual(last.items, localLast.items); assert.equal(last.total, 2); assert.ok(!last.next_cursor);
    const discovered = [...first.items, ...last.items];
    assert.deepEqual(new Set(discovered.map(tool => tool.id)), new Set([config.readToolID, config.writeToolID]));
    for (const tool of discovered) assert.deepEqual(Object.keys(tool).sort(), ['description', 'id', 'name', 'risk', 'version']);
    const selected = await invoke('get_tool_schema', { tool_id: config.readToolID });
    assert.equal(selected.input_schema.type, 'object');
    assert.deepEqual(journal.list(), []);
    const mcpContract=await invoke('get_tool_schema',{tool_id:config.mcpToolID});
    assert.equal(mcpContract.result_format,'mcp_call_tool_result');
    assert.equal(mcpContract.output_schema_scope,'structuredContent_before_projection');
    assert.deepEqual(mcpContract.response_policy.include,['/id']);
  } finally { journal.close(); }
  const selectedPage = await ctx.gateway.search('integration_read');
  assert.equal(selectedPage.items.length, 1); assert.equal(selectedPage.total, 1);
  const op = await ctx.gateway.prepare(config.readToolID, {}, 'read-status');
  assert.equal(op.state, 'READY');
  assert.equal((await ctx.gateway.execute(op.id)).state, 'SUCCEEDED');
  const remoteOp=await ctx.gateway.prepare(config.mcpToolID,{},'remote-mcp-read');
  const remoteResult=await ctx.gateway.execute(remoteOp.id);
  assert.equal(remoteResult.state,'SUCCEEDED');
  assert.equal(remoteResult.result.structuredContent.id,'inventory-1');
  assert.equal(remoteResult.result.structuredContent.next_cursor,'cursor-2');
  assert.ok(!JSON.stringify(remoteResult).includes('must disappear'));
  assert.ok(!JSON.stringify(remoteResult).includes('excluded private'));
  ctx.text('Service is healthy.');
  return { state: 'SUCCEEDED', output: '' };
};
const read = await create('Inspect service health');
assert.equal(await worker.runOnce(), true);
assert.equal((await call(runPath(read))).state, 'SUCCEEDED');
assert.equal((await call(runPath(read))).output, 'Service is healthy.');
const events = (await call(`${runPath(read)}/events?after=0`)).items;
assert.ok(events.some(event => event.type === 'TEXT_DELTA'));
assert.ok(events.every(event => typeof event.id === 'string'));
assert.equal((await call(`${runPath(read)}/events?after=${events.at(-1).id}`)).items.length, 0);

// Approval spans two leases, while durable binding retains the same operation.
let prepared;
execute = async ctx => {
  const op = await ctx.gateway.prepare(config.writeToolID, {}, 'same-write-intent');
  if (ctx.run.attempt === 1) {
    prepared = op.id;
    assert.equal(op.state, 'WAITING_APPROVAL');
    ctx.text('Waiting for an independent approver.');
    return { state: 'WAITING_APPROVAL', output: '', waiting_operation_id: op.id };
  }
  assert.equal(op.id, prepared);
  assert.equal(op.state, 'READY');
  assert.equal((await ctx.gateway.execute(op.id)).state, 'SUCCEEDED');
  assert.equal((await ctx.gateway.execute(op.id)).state, 'SUCCEEDED');
  ctx.text('Approved action completed.');
  return { state: 'SUCCEEDED', output: '' };
};
const write = await create('Perform one approved business action');
await worker.runOnce();
assert.equal((await call(runPath(write))).state, 'WAITING_APPROVAL');
await call(`${runPath(write)}/resume`, {}, config.token, 409);
await call(`/operations/${prepared}/approve`, {}, config.approverToken);
await call(`${runPath(write)}/resume`, {});
await worker.runOnce();
assert.equal((await call(runPath(write))).state, 'SUCCEEDED');
assert.equal((await call(runPath(write))).attempt, 2);
assert.equal((await call(runPath(write))).output, 'Approved action completed.');

// Missing credentials never call inference; explicit resume reuses task state.
ready = false;
execute = async () => { throw new Error('must not execute without credentials'); };
const waiting = await create('Wait for separately configured server credentials');
await worker.runOnce();
assert.equal((await call(runPath(waiting))).state, 'WAITING_CREDENTIALS');
assert.equal((await call('/runs/runtime')).model_ready, false);
ready = true;
execute = async ctx => { ctx.text('Credentials gate cleared by test probe.'); return { state: 'SUCCEEDED', output: '' }; };
await call(`${runPath(waiting)}/resume`, {});
await worker.runOnce();
assert.equal((await call(runPath(waiting))).state, 'SUCCEEDED');

// Cancellation after preparation fences dispatch and a stale successful finish.
execute = async ctx => {
  const op = await ctx.gateway.prepare(config.readToolID, {}, 'cancelled-intent');
  await call(`/runs/${ctx.run.id}/cancel`, {});
  await assert.rejects(() => ctx.gateway.execute(op.id));
  return { state: 'SUCCEEDED', output: 'must not overwrite cancellation' };
};
const cancelled = await create('Cancel before tool dispatch');
await worker.runOnce();
assert.equal((await call(runPath(cancelled))).state, 'CANCELLED');
assert.equal(await worker.runOnce(), false);
console.log('Cloud worker integration passed: real PostgreSQL + Go HTTP + Node worker; paginated REST/leased/Pi discovery, remote MCP projection, success, approval/resume, credentials gate, cancellation; no model request.');
