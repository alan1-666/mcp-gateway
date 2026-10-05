import { randomBytes } from 'node:crypto';
import { mkdir, writeFile, access } from 'node:fs/promises';

await mkdir('.local', { recursive: true, mode: 0o700 });
const path = '.local/identities.json';
try { await access(path); console.log('Identities already exist. No changes made.'); }
catch {
  const roles = ['admin', 'operator', 'approver', 'viewer'];
  const identities = roles.map(role => ({ id: `local-${role}`, workspace_id: 'local', role, token: randomBytes(32).toString('base64url') }));
  await writeFile(path, JSON.stringify(identities, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
  console.log('Created .local/identities.json with separate local identities. Keep this file private.');
}
try { await access('.local/compose.env'); }
catch {
  await writeFile('.local/compose.env', `POSTGRES_PASSWORD=${randomBytes(32).toString('base64url')}\nLOCAL_UID=${process.getuid?.() ?? 1000}\nLOCAL_GID=${process.getgid?.() ?? 1000}\n`, { mode: 0o600, flag: 'wx' });
  console.log('Created private Docker Compose database credentials.');
}
