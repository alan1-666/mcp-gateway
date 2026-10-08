# OpenAPI tool import

The console's **Tool registry → Import OpenAPI** action converts one supported API operation into a reviewable HTTP tool draft. Upload or paste a local JSON document, optionally override the base URL, inspect the supported and unsupported operations, select one, and choose **Review draft**. Edit the normal registration form and create the draft. Publication and client grants use the existing administrative workflow.

Preview runs entirely in the browser. It neither downloads document references nor probes or calls the declared API. Editing the document or base URL invalidates the previous preview; cancelled or superseded file reads cannot replace current input. The parser is loaded only when the import panel opens.

## Supported mapping

This is a conservative mapping profile for OpenAPI 3.0.x and 3.1.x JSON, rather than a complete OpenAPI validator. Parameter and schema behavior follows the [OpenAPI 3.0 specification](https://spec.openapis.org/oas/v3.0.3.html) and [OpenAPI 3.1 specification](https://spec.openapis.org/oas/v3.1.0.html), subject to the gateway HTTP adapter's capabilities.

| Document definition | Generated draft |
| --- | --- |
| A static absolute path | Appended to the selected server base path, preserving `/v1` prefixes |
| Operation, path-item or document `servers` | Most specific declaration takes precedence; the first server is selected with a note when multiple are declared. An explicit base URL overrides that selection |
| GET scalar query parameters | Object schema properties; strings are sent directly, numbers and booleans as JSON scalar query values. Operation parameters override matching inherited parameters |
| POST, PUT, PATCH or DELETE with a required JSON object body | Entire arguments object becomes the JSON request body |
| Supported internal JSON Pointer references | Expanded locally with cycle, depth, size and whole-document work limits |
| OpenAPI 3.0 nullable/exclusive numeric bounds | Converted to equivalent JSON Schema constraints; nullable query parameters and nullable root bodies remain unsupported |
| Header API keys or HTTP basic/bearer authentication | Require an existing managed credential reference in the review form. No secrets are copied; actual header values and origin binding must be configured separately |
| `operationId` | Used as the name when it satisfies the gateway name rules; otherwise a deterministic fallback is generated. Duplicate generated names block the affected operations |
| Summary and description | Preserved as the tool description, bounded to 4,000 UTF-8 bytes |

All imported drafts begin with **write risk and required approval**, including GET operations. The administrator reviews the actual business behavior before selecting a different risk classification or publishing an approval exemption. The importer does not infer grants or exemptions from HTTP method, names, or annotations.

Every documented explicit success response must declare only `application/json`. Output schemas are **not imported**: inspect and enter the desired response contract in the normal form. An accepted preview does not prove upstream compatibility or a business postcondition. The API remains authoritative for schema compilation, name uniqueness, credentials, permissions and origin policy.

## Explicit unsupported cases

Operations remain visible with an explanation when they require dynamic path substitution, header/cookie arguments, array/object query serialization, a GET body, mixed query/body parameters, optional bodies, non-JSON bodies, empty/wildcard success responses, callbacks, unsupported HTTP methods, external/cyclic references, reference siblings, custom schema dialects or schema keywords outside the mapping profile. Read-only/write-only schema semantics, advanced regular expressions, query/cookie credentials, OAuth scopes and alternative authentication requirements require manual mapping.

YAML, URL-based document fetching and automatic bulk registration are not supported. Default values are annotations; the adapter does not inject them. Importing a document never expands the deployment origin/CIDR allowlist, provisions an OAuth client, or creates downstream credentials.

## Limits and verification

- Source: 1 MiB UTF-8, 100,000 JSON tokens and nesting depth 48. Duplicate object keys, unsafe property names, NUL and numeric values that cannot survive browser parsing are rejected
- Operations: at most 256, with at most 128 parameters per declaration
- References: at most 24 reference steps; expanded schemas at most 32 levels, 2,048 nodes per conversion and 64 KiB per operation
- Whole-document expansion: 32,768 work units and 4 MiB of copied schema content; exhaustion rejects the entire preview instead of returning a misleading partial list
- Credentials: at most 16 declared headers, each at most 128 characters; conflicting and reserved headers are rejected

The parser has an independent CI gate of 90% lines, branches and functions. Tests cover malformed data, internal references, schema conversions, unsupported request mappings, authentication, URL/path handling and reference amplification. A cross-language integration uses the actual TypeScript parser, Go API, isolated PostgreSQL schema and HTTP fixture to verify generated query/body requests, draft visibility, schema rejection, independent approval and idempotent dispatch. Browser and cloud evidence are recorded separately in [verification](verification.md).
