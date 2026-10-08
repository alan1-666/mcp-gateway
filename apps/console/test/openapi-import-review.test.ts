import assert from "node:assert/strict";
import test from "node:test";
import { previewOpenAPI } from "../src/openapi-import";

const jsonResponse = { "200": { content: { "application/json": {} } } };
function document(
  operation: Record<string, unknown>,
  components?: Record<string, unknown>,
) {
  return JSON.stringify({
    openapi: "3.1.0",
    servers: [{ url: "https://api.example.test/v1" }],
    paths: { "/search": { get: { responses: jsonResponse, ...operation } } },
    ...(components ? { components } : {}),
  });
}
function unsupported(source: string) {
  const [operation] = previewOpenAPI(source);
  assert.ok(operation, "operation must remain visible for review");
  assert.equal(
    operation.candidate,
    undefined,
    "incompatible operation must not be advertised as supported",
  );
  assert.ok(operation.reason, "operator needs an explicit unsupported reason");
}

test("import rejects authentication headers that managed credentials cannot supply", () => {
  for (const name of [
    "Proxy-Authorization",
    "Proxy-Connection",
    "TE",
    "Trailer",
    "Upgrade",
    "Mcp-Session-Id",
    "MCP-Protocol-Version",
    "X".repeat(129),
  ]) {
    unsupported(
      document(
        { security: [{ key: [] }] },
        {
          securitySchemes: { key: { type: "apiKey", in: "header", name } },
        },
      ),
    );
  }
  const schemes = Object.fromEntries(
    Array.from({ length: 17 }, (_, i) => [
      `key${i}`,
      { type: "apiKey", in: "header", name: `X-Key-${i}` },
    ]),
  );
  unsupported(
    document(
      {
        security: [
          Object.fromEntries(Object.keys(schemes).map((key) => [key, []])),
        ],
      },
      {
        securitySchemes: schemes,
      },
    ),
  );
});

test("import accepts ordinary origin-bound header authentication", () => {
  const [operation] = previewOpenAPI(
    document(
      { security: [{ key: [] }] },
      {
        securitySchemes: {
          key: { type: "apiKey", in: "header", name: "X-API-Key" },
        },
      },
    ),
  );
  assert.equal(operation.candidate?.credentialRequired, true);
  assert.equal(operation.candidate?.url, "https://api.example.test/v1/search");
});

test("import rejects JavaScript regex syntax that the gateway Go schema compiler cannot accept", () => {
  for (const pattern of ["\\u0041", "\\cA", "a{1001}", "(a{100}){100}"]) {
    unsupported(
      document({
        parameters: [
          { in: "query", name: "query", schema: { type: "string", pattern } },
        ],
      }),
    );
  }
});

test("small documents cannot amplify shared schemas across all operations without a global budget", () => {
  const shared = {
    get: {
      responses: jsonResponse,
      parameters: [
        {
          in: "query",
          name: "query",
          schema: { $ref: "#/components/schemas/Large" },
        },
      ],
    },
  };
  const source = JSON.stringify({
    openapi: "3.1.0",
    servers: [{ url: "https://api.example.test" }],
    components: {
      schemas: { Large: { type: "string", description: "x".repeat(60000) } },
      pathItems: { Shared: shared },
    },
    paths: Object.fromEntries(
      Array.from({ length: 256 }, (_, i) => [
        `/path${i}`,
        { $ref: "#/components/pathItems/Shared" },
      ]),
    ),
  });
  assert.ok(new TextEncoder().encode(source).length < 100000);
  assert.throws(
    () => previewOpenAPI(source),
    /OpenAPI reference expansion exceeds the document budget\./,
  );
});

test("the document expansion budget includes repeated parameter names", () => {
  const source = JSON.stringify({
    openapi: "3.1.0",
    servers: [{ url: "https://api.example.test" }],
    components: {
      pathItems: {
        Shared: {
          get: {
            responses: jsonResponse,
            parameters: [
              {
                in: "query",
                name: "query".repeat(12000),
                schema: { type: "string" },
              },
            ],
          },
        },
      },
    },
    paths: Object.fromEntries(
      Array.from({ length: 256 }, (_, i) => [
        `/path${i}`,
        { $ref: "#/components/pathItems/Shared" },
      ]),
    ),
  });
  assert.ok(new TextEncoder().encode(source).length < 100000);
  assert.throws(
    () => previewOpenAPI(source),
    /OpenAPI reference expansion exceeds the document budget\./,
  );
});
