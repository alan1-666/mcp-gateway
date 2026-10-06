# Invocation and approval policy

Rillgate exposes `call_tool` over MCP and `POST /api/v1/call` over HTTP. Both accept
`tool_id`, an `arguments` object and a caller-generated `idempotency_key` of 8–128
UTF-8 bytes. The key is mandatory for reads and writes and binds one workspace,
actor, tool and canonical argument hash. Reuse it when the response is uncertain.

The facade prepares the intent and executes it only when READY. It returns the
operation ID and state. WAITING_APPROVAL, DISPATCHING, UNKNOWN and terminal states
are returned without starting another execution. After approval, repeating the
same intent resumes that operation. Legacy prepare/invoke interfaces remain
available. A successful HTTP/MCP transport response is not a successful business
operation: callers must inspect `state`.

## Tool policy

Only an authenticated workspace administrator can change the policy:

```http
POST /api/v1/tools/{tool_id}/approval-policy
Content-Type: application/json

{"expected_version": 3, "approval_policy": "none"}
```

- `required`: a different human must approve the fixed operation before dispatch;
  the approval expires after 30 minutes.
- `none`: the ordinary execution checks permit direct invocation without approval.
  This does not change a write tool into a read tool or grant additional access.

Existing definitions without a policy retain conservative defaults: writes require
approval; reads do not. Unknown policy values fail closed. Callers cannot choose a
policy in invocation arguments. Initial tool creation/import does not accept an
approval exemption; granting one requires an explicit policy revision.

The update checks `expected_version`, including for no-ops. A real update increments
the tool version and atomically records old/new policy and version in the audit log.
Version history includes the effective policy. Unrelated definition revisions and
definition rollbacks preserve the current policy rather than restoring an obsolete
exemption. Changing the side-effect classification restores the conservative default
for the new classification and shows that policy change in the candidate diff.

## Dispatch and concurrent changes

Preparation stores immutable tool and argument snapshots. Claim serializes with
policy updates on the tool row and rechecks the latest grants, enabled state and
policy. Both the snapshot and current policy must permit dispatch. An unapproved
write exemption also requires the original tool version to remain current; even a
tighten-then-loosen sequence sends a stale READY intent to WAITING_APPROVAL.

Loosening a policy never automatically executes or reclassifies an existing pending
approval. Granting approval does not overwrite its original arguments or tool
snapshot. Existing approvals retain their expiry and independent reviewer checks.
Connector jobs repeat policy/version checks at enqueue and at the final Start grant.

Claim is the public HTTP/MCP execution authorization boundary; Connector Start is
the additional private execution boundary. Changes committed after that boundary
cannot recall a request that may already have reached the upstream. No policy
update promises cancellation of an in-flight side effect.

Ambiguous writes remain UNKNOWN and are not automatically replayed, including with
a permissive approval policy. Review downstream evidence before deciding whether a
new business intent is appropriate.

## Migration and rollback

Migration `015_tool_approval_policy.sql` retains the database independent-approval
constraint for legacy writes and permits an exemption only when the immutable
operation snapshot explicitly records `approval_policy=none`. Existing snapshots,
approvers and arguments are not rewritten.

The schema migration is additive in capability and has no destructive down migration.
Do not restore the old constraint over retained exempt write history or delete that
history to make a rollback succeed. An application rollback can keep this schema;
older code conservatively requires approval and does not understand new exemptions.
Before rolling back, stop admitting new work and review pending exempt operations;
older code can reject these instead of continuing them. Preserve operation/audit
records when recovering service.

## Verification

Core policy functions, policy persistence, HTTP policy handling and the invocation
facade have 100% statement coverage in the current targeted Go coverage run. This is
not a claim of 100% repository coverage. Tests cover default/explicit policies,
management authorization, optimistic conflicts, transaction rollback, simultaneous
calls, policy-versus-claim locking, tighten/loosen changes, immutable snapshots,
UNKNOWN replay prevention, Connector Start fencing, definition rollback and actual
HTTP/standard MCP SDK calls. Real business write-side acceptance remains separate
from these isolated test adapters and fixtures.
