import { resolve } from "node:path";
import { parseArgs } from "node:util";
import { runAgent, safeError } from "./runtime.js";

const HELP = `MCP Gateway Pi Runner

Usage:
  npm run run --workspace @mcp-gateway/agent-runner -- --check
  npm run run --workspace @mcp-gateway/agent-runner -- --prompt "Investigate failed jobs"
  npm run run --workspace @mcp-gateway/agent-runner -- --session <id> --prompt "Continue"

Environment:
  GATEWAY_URL         Gateway origin; defaults to http://127.0.0.1:8090
  GATEWAY_TOKEN       Required operator token; never passed to the model
  PI_PROVIDER        Optional explicit subscription provider
  PI_MODEL           Optional explicit model ID (no API-key fallback)
  GATEWAY_STATE_DIR   Local journal/session directory; defaults to <repository>/.gateway

Options:
  --check            Inspect configured subscription login and gateway identity; no model request
  --prompt <text>    Send one user request (or supply a positional message)
  --session <id>     Resume a session created by this runner
  --help            Show usage
`;

try {
  const { values, positionals } = parseArgs({ options: { check: { type: "boolean" }, prompt: { type: "string" }, session: { type: "string" }, help: { type: "boolean" } }, allowPositionals: true });
  if (values.help || (!values.check && !values.prompt && !positionals.length)) process.stdout.write(HELP);
  else await runAgent({ check: values.check ?? false, prompt: values.prompt ?? positionals.join(" "), session: values.session, stateDir: process.env.GATEWAY_STATE_DIR ?? resolve(import.meta.dirname, "../../..", ".gateway") });
} catch (error) { process.stderr.write(`${safeError(error)}\n`); process.exitCode = 1; }
