# Implementation status

The [product design](product-design.md) and [system architecture](architecture.md) describe the intended production system. This page describes code that exists and identifies the remaining work without treating architecture targets as delivered capabilities.

## Implemented

| Area | Working behavior |
| --- | --- |
| Services | Separate Go API, MCP gateway, operation recovery worker and migration commands |
| Persistence | PostgreSQL tool definitions, immutable operation snapshots, transactional states/events, checked migrations |
| Access | Invitation-only cloud accounts, hashed passwords, CSRF-protected sessions, expiring/revocable API keys, four roles and separate approving users |
| Team | Single-use invitations, member disablement/role changes, credential revocation and identity audit history |
| Registry | Manual HTTP tool registration, JSON Schema validation, publication and immediate database-backed disable checks |
| Execution | Prepare → approve when needed → atomic claim → HTTP execution → durable result; idempotency conflict checks |
| Uncertainty | Ambiguous writes and interrupted dispatches become `UNKNOWN`; no automatic side-effect replay |
| HTTP adapter | Fixed origins and credentials, checked DNS dialing, blocked redirects/metadata addresses, bounded JSON results and basic secret field redaction |
| MCP | Official Go SDK v1.7.0, stateless Streamable HTTP, five governed discovery/execution tools |
| Console | Real registry, prepare/execute controls, approval inbox, operation details and event history; cloud sign-in and team/account administration |
| Pi | User-hosted subscription-backed CLI with restricted tools, persistent local intent journal and explicit approval/unknown pauses |
| Delivery | Locked dependencies, source startup, Compose configuration, backend/container build definitions, OpenAPI contract and CI checks |

## Remaining production architecture

- Enterprise OIDC/OAuth interoperability, MFA, self-service account recovery and short-lived Runner identity.
- Multiple organization/workspace membership, database RLS and per-service database accounts.
- Full tool version lifecycle, signed releases, instance acknowledgements and rollback.
- OpenAPI/Protobuf import, gRPC and upstream MCP adapters, outbound private-network Connector.
- Paginated/semantic discovery and configurable field-level access policies.
- Durable platform Agent Runs, gRPC Runner control, leases/fencing, central budgets, task cancellation and checkpoint reconciliation.
- Result-query adapters, verified human outcome reconciliation and compensation workflows.
- S3 artifacts, Redis limits/cache, durable outbox consumers, SSE and webhook delivery.
- Structured evaluation datasets, production release gates, telemetry dashboards and performance measurements.
- Kubernetes deployment, off-host disaster recovery, point-in-time recovery, retention/deletion jobs and signed image delivery.

The cloud deployment adds a concrete operational baseline; local Pi sessions and a single host do not establish the remaining production capabilities. Release readiness must be based on completed acceptance tests against the [architecture's targets](architecture.md).

## Current constraints

- Cloud accounts currently belong to one workspace. Invite possession grants the selected role; email ownership is not verified.
- Cloud Agent task execution and model account onboarding remain pending. The Gateway can already serve external MCP clients.
- Backups are host-local, and the deployment has no high-availability or external alert delivery.

- HTTP JSON tools only; paths are static and streaming/non-JSON tools are rejected.
- All write tools require independent approval. Approval lasts 30 minutes.
- Tools list at most 500 records, operations list the latest 200, and events read in batches of 500 using an `after` cursor. There is no full list pagination yet.
- Only one registered content version exists per tool. Editing a published definition is unavailable.
- Without an output schema, successful execution verifies HTTP/JSON delivery only. Business postconditions require an explicit output contract and, when appropriate, a future outcome-query adapter.
- Secret field filtering is not comprehensive DLP. Argument/result retention requires further policy implementation.
- MCP resource, prompt, sampling and resumable transport features are not advertised.
- The Pi CLI does not claim distributed task durability. It refuses to redispatch uncertain operations and requires the operator to inspect their state.
