import { spawn } from 'node:child_process';

const commands = [
  ['api', './bin/api-server', []],
  ['gateway', './bin/gateway', []],
  ['worker', './bin/worker', []],
  ['console', 'npm', ['run', 'dev:console']],
];
let stopping = false;
const children = [];
function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  for (const child of children) child.kill('SIGTERM');
}
for (const [name, command, args] of commands) {
  const child = spawn(command, args, { stdio: 'inherit' });
  children.push(child);
  child.on('error', () => { console.error(`${name} could not start`); stop(1); });
  child.on('exit', code => { if (!stopping) { console.error(`${name} exited (${code ?? 'signal'})`); stop(code || 1); } });
}
process.once('SIGINT', () => stop());
process.once('SIGTERM', () => stop());
