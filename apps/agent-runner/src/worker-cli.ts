import { readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { parseArgs } from "node:util";
import { VERSION } from "@earendil-works/pi-coding-agent";
import { WorkerClient, WorkerError } from "./worker-client.js";
import { acquireWorkerState } from "./worker-state.js";
import { CloudWorker, safeWorkerCode } from "./worker.js";

const usage = `MCP Gateway cloud Pi worker

Usage: npm run worker --workspace @mcp-gateway/agent-runner -- [--once | --check]

Required: RUNNER_API_URL, RUNNER_SHARED_SECRET_FILE
Optional: RUNNER_WORKER_ID, GATEWAY_STATE_DIR, PI_PROVIDER, PI_MODEL
          PI_CODING_AGENT_DIR, RUNNER_HTTP_ALLOWED_HOSTS

--once   Publish runtime status, claim at most one task, then exit.
--check  Publish sanitized local model configuration status; do not claim or infer.
Default  Poll for tasks until SIGINT/SIGTERM; one task at a time.

Use a dedicated persistent state directory and Pi login volume. Shared secrets
must never be passed as command-line arguments. Remote HTTP requires an explicit
RUNNER_HTTP_ALLOWED_HOSTS hostname; HTTPS and loopback are supported by default.
`;

let ownership: ReturnType<typeof acquireWorkerState> | undefined;
try {
  const { values } = parseArgs({ options: { once: { type: "boolean" }, check: { type: "boolean" }, help: { type: "boolean" } } });
  if (values.help) process.stdout.write(usage);
  else {
    if (values.once && values.check) throw new WorkerError("CHOOSE_ONCE_OR_CHECK");
    const path = process.env.RUNNER_SHARED_SECRET_FILE;
    if (!path || statSync(path).size > 4096) throw new WorkerError("RUNNER_SHARED_SECRET_FILE_REQUIRED");
    const secret = readFileSync(path, "utf8").trim();
    const url = process.env.RUNNER_API_URL;
    if (!url) throw new WorkerError("RUNNER_API_URL_REQUIRED");
    const api = new WorkerClient(url, secret, (process.env.RUNNER_HTTP_ALLOWED_HOSTS ?? "").split(",").map(value => value.trim()).filter(Boolean));
    const stateDir = resolve(process.env.GATEWAY_STATE_DIR ?? resolve(import.meta.dirname, "../../..", ".gateway-worker"));
    ownership = acquireWorkerState(stateDir, process.env.RUNNER_WORKER_ID);
    const worker = new CloudWorker({ api, stateDir, workerId: ownership.workerId, recoverIntentLock: ownership.recoverIntentLock, log: event => process.stdout.write(`${JSON.stringify(event)}\n`) });
    const stop = new AbortController();
    const interrupt = () => stop.abort();
    process.once("SIGINT", interrupt); process.once("SIGTERM", interrupt);
    try {
      if (values.check) process.stdout.write(`${JSON.stringify({ ...(await worker.check(stop.signal)), pi_sdk: VERSION, model_request_sent: false, note: "model_ready describes local configuration, not verified subscription validity or quota" })}\n`);
      else if (values.once) await worker.runOnce(stop.signal);
      else await worker.run(stop.signal);
    } finally { process.removeListener("SIGINT", interrupt); process.removeListener("SIGTERM", interrupt); }
  }
} catch (error) {
  const code = safeWorkerCode(error);
  process.stderr.write(`${JSON.stringify({ event: "worker_stopped", error_code: code })}\n`);
  if (code.includes("LOCK") || code.includes("STATE_IN_USE")) process.stderr.write("Stop all workers sharing this state volume. Verify the recorded PID/start identity before removing a stale worker.lock or worker-lock-recovery directory; preserve Run sessions and intent journals.\n");
  process.exitCode = 1;
} finally { try { ownership?.close(); } catch { process.stderr.write('{"event":"worker_lock_cleanup_failed"}\n'); process.exitCode = 1; } }
