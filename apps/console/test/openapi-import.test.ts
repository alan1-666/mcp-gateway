import assert from "node:assert/strict";
import test from "node:test";
import { MAX_OPENAPI_BYTES, previewOpenAPI } from "../src/openapi-import.ts";

const responses = () => ({
  "200": { content: { "application/json": { schema: { type: "object" } } } },
});
const op = (extra: Record<string, unknown> = {}) => ({
  operationId: "findItems",
  responses: responses(),
  ...extra,
});
const doc = (
  operation: unknown = op(),
  method = "get",
  path = "/items",
): any => ({
  openapi: "3.0.3",
  servers: [{ url: "https://api.example.com/v1" }],
  paths: { [path]: { [method]: operation } },
});
const query = (schema: unknown, extra: Record<string, unknown> = {}) => ({
  name: "q",
  in: "query",
  schema,
  ...extra,
});
const body = (schema: unknown, extra: Record<string, unknown> = {}) =>
  op({
    requestBody: {
      required: true,
      content: { "application/json": { schema } },
      ...extra,
    },
  });
const preview = (d: unknown, override?: string) =>
  previewOpenAPI(JSON.stringify(d), override);
function good(d: unknown, override?: string) {
  const rows = preview(d, override);
  assert.equal(rows.length, 1);
  assert.ok(rows[0].candidate, rows[0].reason);
  return rows[0].candidate!;
}
function bad(d: unknown, reason: RegExp, override?: string) {
  const rows = preview(d, override);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].candidate, undefined);
  assert.match(rows[0].reason!, reason);
}
function schemaBad(schema: unknown, reason: RegExp) {
  bad(
    doc(body({ type: "object", properties: { field: schema } }), "post"),
    reason,
  );
}

test("offline GET preview preserves query constraints, base path and review warnings", () => {
  const d = doc(
    op({
      summary: "Search",
      description: "Find items",
      parameters: [
        query({ type: "string", minLength: 1 }, { required: true }),
        query({ type: "integer", minimum: 1, maximum: 20 }, { name: "limit" }),
        query({ type: "boolean" }, { name: "active" }),
      ],
    }),
  );
  const before = JSON.stringify(d),
    c = good(d);
  assert.equal(c.id, "GET /items");
  assert.equal(c.url, "https://api.example.com/v1/items");
  assert.equal(c.name, "findItems");
  assert.equal(c.description, "Search\n\nFind items");
  assert.equal(c.credentialRequired, false);
  assert.deepEqual(JSON.parse(JSON.stringify(c.inputSchema)), {
    type: "object",
    properties: {
      q: { type: "string", minLength: 1 },
      limit: { type: "integer", minimum: 1, maximum: 20 },
      active: { type: "boolean" },
    },
    required: ["q"],
    additionalProperties: false,
  });
  for (const text of [
    "Output schemas are not imported",
    "write-risk drafts",
    "defaults are annotations",
  ])
    assert.ok(c.warnings.some((x) => x.includes(text)));
  assert.equal(JSON.stringify(d), before);
});

test("server precedence and explicit override retain OpenAPI base paths", () => {
  const d = doc(op({ servers: [{ url: "http://localhost:8080/op/" }] }));
  d.paths["/items"].servers = [{ url: "https://path.example/a" }];
  assert.equal(good(d).url, "http://localhost:8080/op/items");
  delete d.paths["/items"].get.servers;
  assert.equal(good(d).url, "https://path.example/a/items");
  assert.equal(
    good(d, "https://override.example/prefix").url,
    "https://override.example/prefix/items",
  );
  const first = doc();
  first.servers.push({ url: "https://second.example" });
  assert.ok(
    good(first).warnings.some((x) => x.includes("first declared server")),
  );
  for (const servers of [undefined, [], null, {}])
    bad({ ...doc(), servers }, /server URL/);
  bad(doc(op({ servers: null })), /server URL/);
  for (const url of [
    "relative",
    "ftp://host",
    "https://host/?q=x",
    "https://host/#frag",
    "https://{host}",
    "https://host/a b",
    "https://host/\\x",
  ])
    bad(doc(), /absolute HTTP/, url);
  bad(doc(), /credentials/, "https://user:secret@example.com");
  bad(doc(), /Invalid server URL/, "https://");
});

test("supported methods require faithful body presence and unsupported operations remain visible", () => {
  for (const method of ["post", "put", "patch", "delete"]) {
    const c = good(
      doc(
        body({
          type: "object",
          properties: { job: { type: "string" } },
          required: ["job"],
          additionalProperties: false,
        }),
        method,
      ),
    );
    assert.equal(c.method, method.toUpperCase());
    bad(doc(op(), method), /always sends/);
  }
  for (const method of ["head", "options", "trace"])
    bad(doc(op(), method), /HTTP method/);
  assert.deepEqual(
    preview({
      ...doc(),
      paths: { "/nothing": { summary: "metadata" }, "x-extra": { get: op() } },
    }),
    [],
  );
  const c = good(
    doc(op({ operationId: "invalid name!", summary: 12, description: false })),
  );
  assert.equal(c.name, "api_get_1");
  assert.equal(c.description, "GET /items");
  bad(doc(op({ description: "中".repeat(1400) })), /4000 bytes/);
  bad(doc(false), /OpenAPI object/);
  const duplicate = preview({
    ...doc(),
    paths: {
      "/one": { get: op() },
      "/two": { post: body({ type: "object" }) },
    },
  });
  assert.ok(
    duplicate.every(
      (x) =>
        !x.candidate && x.reason?.includes("Duplicate generated tool names"),
    ),
  );
});

test("parameter inheritance supports override but refuses incompatible serialization", () => {
  const d = doc(
    op({ parameters: [query({ type: "integer" }, { required: true })] }),
  );
  d.paths["/items"].parameters = [
    query({ type: "string" }),
    query(
      { type: "number" },
      {
        name: "other",
        style: "form",
        explode: false,
        allowReserved: false,
        allowEmptyValue: false,
      },
    ),
  ];
  const c = good(d);
  assert.equal((c.inputSchema.properties as any).q.type, "integer");
  assert.deepEqual(c.inputSchema.required, ["q"]);
  for (const extra of [
    { in: "path" },
    { in: "header" },
    { in: "cookie" },
    { content: {} },
    { style: "deepObject" },
    { explode: "yes" },
    { allowReserved: true },
    { allowEmptyValue: true },
  ])
    bad(
      doc(op({ parameters: [query({ type: "string" }, extra)] })),
      /scalar query/,
    );
  for (const type of ["array", "object", "null"])
    bad(
      { ...doc(op({ parameters: [query({ type })] })), openapi: "3.1.1" },
      /Query arrays/,
    );
  bad(
    doc(op({ parameters: [query({ type: "string", nullable: true })] })),
    /nullable/,
  );
  bad(
    doc(op({ parameters: [query({ type: "string" }, { required: "yes" })] })),
    /required must/,
  );
  for (const parameters of [{}, Array(129).fill(query({ type: "string" }))])
    bad(doc(op({ parameters })), /bounded array/);
  for (const p of [
    null,
    {},
    query({ type: "string" }, { name: "" }),
    query({ type: "string" }, { name: "__proto__" }),
    query({ type: "string" }, { in: 2 }),
  ])
    bad(doc(op({ parameters: [p] })), /object|Invalid parameter/);
  bad(
    doc(
      op({
        parameters: [query({ type: "string" }), query({ type: "string" })],
      }),
    ),
    /Duplicate parameters/,
  );
  bad(doc(op({ requestBody: {} })), /GET request bodies/);
  bad(
    doc(
      { ...body({ type: "object" }), parameters: [query({ type: "string" })] },
      "post",
    ),
    /cannot include query/,
  );
});

test("body media, optionality and object shape must match the HTTP adapter", () => {
  for (const required of [false, undefined, "true"])
    bad(
      doc(body({ type: "object" }, { required }), "post"),
      /Optional request bodies/,
    );
  for (const content of [
    {},
    { "text/plain": {} },
    { "application/json": {}, "application/xml": {} },
    { "application/json": null },
  ])
    bad(
      doc(body({ type: "object" }, { content }), "post"),
      /application\/json/,
    );
  bad(
    doc(
      body(
        { type: "object" },
        {
          content: {
            "application/json": { schema: { type: "object" }, encoding: {} },
          },
        },
      ),
      "post",
    ),
    /encoding/,
  );
  for (const schema of [
    { type: "array" },
    { type: "string" },
    { type: "object", nullable: true },
  ])
    bad(doc(body(schema), "post"), /non-null JSON object/);
  for (const extra of [{ callbacks: {} }, { webhooks: {} }])
    bad(doc(op(extra)), /Callbacks/);
});

test("every declared success must be JSON while error responses may differ", () => {
  for (const r of [{}, { "400": {} }, { default: {} }])
    bad(doc(op({ responses: r })), /JSON success response/);
  for (const code of ["204", "205", "2XX", "2xx"])
    bad(
      doc(op({ responses: { ...responses(), [code]: {} } })),
      /Empty or wildcard/,
    );
  for (const content of [
    {},
    { "text/plain": {} },
    { "application/json": false },
    { "application/json": {}, "text/plain": {} },
  ])
    bad(doc(op({ responses: { "200": { content } } })), /application\/json/);
  good(
    doc(
      op({
        responses: { ...responses(), "400": { content: { "text/plain": {} } } },
      }),
    ),
  );
  for (const r of [null, { "200": {} }])
    bad(doc(op({ responses: r })), /OpenAPI object/);
});

test("local references resolve escaped parameter/schema/body/response/path pointers", () => {
  const d = doc(
    op({
      parameters: [{ $ref: "#/components/parameters/query" }],
      responses: { "200": { $ref: "#/components/responses/ok" } },
    }),
  );
  d.components = {
    parameters: {
      query: {
        ...query({ $ref: "#/components/schemas/a~1b~0c" }),
        required: true,
      },
    },
    schemas: { "a/b~c": { type: "string", enum: ["a", "b"] } },
    responses: { ok: { content: { "application/json": {} } } },
  };
  assert.deepEqual((good(d).inputSchema.properties as any).q, {
    type: "string",
    enum: ["a", "b"],
  });
  d.paths["/items"] = {
    post: op({ requestBody: { $ref: "#/components/requestBodies/item" } }),
  };
  d.components.requestBodies = {
    item: {
      required: true,
      content: {
        "application/json": { schema: { $ref: "#/components/schemas/object" } },
      },
    },
  };
  d.components.schemas.object = {
    type: "object",
    properties: {
      left: { $ref: "#/components/schemas/a~1b~0c" },
      right: { $ref: "#/components/schemas/a~1b~0c" },
    },
  };
  assert.equal((good(d).inputSchema.properties as any).right.type, "string");
  d.components.path = d.paths["/items"];
  d.paths["/items"] = { $ref: "#/components/path" };
  good(d);
});

test("references reject remote, missing, sibling, cyclic and excessive expansion contracts", () => {
  for (const ref of ["https://remote/schema", "#", 5])
    schemaBad({ $ref: ref }, /Only local/);
  for (const ref of ["#/%zz", "#/bad~2escape", "#/bad~"])
    schemaBad({ $ref: ref }, /Invalid local/);
  schemaBad({ $ref: "#/missing" }, /not found/);
  schemaBad({ $ref: "#/components/x", type: "string" }, /sibling/);
  const d = doc(body({ $ref: "#/components/a" }), "post");
  d.components = {
    a: { type: "object", properties: { self: { $ref: "#/components/a" } } },
  };
  bad(d, /Cyclic/);
  d.components = {
    a: { $ref: "#/components/b" },
    b: { $ref: "#/components/a" },
  };
  bad(d, /Cyclic/);
  d.components = { a: [] };
  bad(d, /OpenAPI object/);
  d.components = { a: { $ref: "#/components/r0" } };
  for (let i = 0; i < 25; i++)
    d.components["r" + i] = { $ref: "#/components/r" + (i + 1) };
  d.components.r25 = { type: "object" };
  bad(d, /deeply nested/);
  d.components = { a: { type: "object", properties: {} } };
  for (let i = 0; i < 2100; i++)
    d.components.a.properties["p" + i] = { type: "string" };
  bad(d, /complexity limit/);
  d.components = { a: { type: "object", description: "a".repeat(65536) } };
  bad(d, /64 KiB/);
  // Inline schema depth is independently bounded below document depth.
  let deep: any = { type: "object" };
  for (let i = 0; i < 34; i++) deep = { type: "array", items: deep };
  schemaBad(deep, /complexity limit/);
  assert.throws(
    () =>
      preview({ ...doc(), paths: { "/bad": { $ref: "https://external" } } }),
    /Path item references/,
  );
});

test("OpenAPI 3.0 nullable and exclusive bounds convert without dropping constraints", () => {
  const schema = {
    type: "object",
    properties: {
      score: {
        type: "number",
        minimum: 1,
        maximum: 10,
        exclusiveMinimum: true,
        exclusiveMaximum: false,
        nullable: true,
        enum: [2, 3],
      },
      list: {
        type: "array",
        items: { type: "integer" },
        minItems: 1,
        uniqueItems: true,
      },
      dictionary: { type: "object", additionalProperties: { type: "boolean" } },
    },
    required: ["score"],
  };
  const c = good(doc(body(schema), "post")).inputSchema as any;
  assert.deepEqual(c.properties.score, {
    type: ["number", "null"],
    maximum: 10,
    exclusiveMinimum: 1,
    enum: [2, 3],
  });
  assert.equal(c.properties.dictionary.additionalProperties.type, "boolean");
  assert.equal(
    good(
      doc(
        body({ type: "object", nullable: false, additionalProperties: false }),
        "post",
      ),
    ).inputSchema.nullable,
    undefined,
  );
  const v31 = doc(
    body({
      type: "object",
      properties: {
        a: { type: "number", exclusiveMinimum: 1, exclusiveMaximum: 9 },
        nothing: { type: "null" },
      },
    }),
    "post",
  );
  v31.openapi = "3.1.0";
  good(v31);
  for (const s of [
    { type: "number", exclusiveMinimum: 1 },
    { type: "number", exclusiveMinimum: true },
    { type: "number", exclusiveMaximum: "no" },
  ])
    schemaBad(s, /exclusive bound/);
  for (const s of [
    { type: "string", nullable: "true" },
    { type: "null" },
    { type: ["string", "null"] },
  ])
    schemaBad(s, /nullable|single type/);
  bad(
    {
      ...doc(body({ type: "object", nullable: true }), "post"),
      openapi: "3.1.0",
    },
    /nullable/,
  );
  bad(
    {
      ...doc(
        body({
          type: "object",
          properties: { a: { type: "number", exclusiveMinimum: true } },
        }),
        "post",
      ),
      openapi: "3.1.0",
    },
    /exclusive bound/,
  );
});

test("unsupported or malformed schemas cannot silently lose validation", () => {
  for (const s of [
    { type: "string", unknown: 1 },
    { type: "object", allOf: [] },
    { type: "string", $schema: "custom" },
  ])
    schemaBad(s, /unsupported keywords/);
  for (const k of ["readOnly", "writeOnly"])
    schemaBad({ type: "string", [k]: true }, /semantics/);
  for (const k of [
    "title",
    "description",
    "format",
    "readOnly",
    "writeOnly",
    "uniqueItems",
    "deprecated",
    "examples",
  ])
    schemaBad({ type: "string", [k]: 123 }, /annotations/);
  for (const s of [
    { type: "string", format: "binary" },
    { type: "number", format: "date" },
  ])
    schemaBad(s, /format/);
  good(
    doc(
      op({
        parameters: [
          query({
            type: "string",
            format: "date",
            default: "2026-01-01",
            examples: ["2026-01-02"],
            example: "2026-01-03",
            readOnly: false,
            writeOnly: false,
            deprecated: false,
          }),
        ],
      }),
    ),
  );
  for (const s of [
    { type: "number", minimum: "1" },
    { type: "number", multipleOf: 0 },
    { type: "string", minLength: -1 },
    { type: "string", maxLength: 1.5 },
    { type: "array", minItems: -1 },
    { type: "object", maxProperties: "many" },
  ])
    schemaBad(s, /numeric constraint/);
  for (const pattern of [5, "["])
    schemaBad({ type: "string", pattern }, /invalid pattern/);
  for (const pattern of ["(?=a)", "(.)\\1"])
    schemaBad({ type: "string", pattern }, /manual validation/);
  good(
    doc(
      op({
        parameters: [
          query({
            type: "string",
            pattern: "^[a-z]+$",
            minLength: 1,
            maxLength: 50,
          }),
        ],
      }),
    ),
  );
  for (const value of [[], ["a", "a"], "a"])
    schemaBad({ type: "string", enum: value }, /invalid enum/);
  for (const s of [
    { type: "string", properties: {} },
    { type: "string", additionalProperties: true },
  ])
    schemaBad(s, /Object constraints/);
  for (const required of ["a", [1], ["a", "a"]])
    schemaBad({ type: "object", required }, /required properties/);
  schemaBad({ type: "string", required: ["a"] }, /required properties/);
  schemaBad({ type: "string", items: { type: "string" } }, /Array constraints/);
});

test("authentication flags header credentials without copying secrets or choosing alternatives", () => {
  for (const scheme of [
    { type: "apiKey", in: "header", name: "X-API-Key" },
    { type: "http", scheme: "bearer" },
    { type: "http", scheme: "basic" },
  ]) {
    const d = {
      ...doc(),
      security: [{ auth: [] }],
      components: { securitySchemes: { auth: scheme } },
    };
    const c = good(d);
    assert.equal(c.credentialRequired, true);
    assert.ok(c.warnings.some((x) => x.includes("No authentication secrets")));
    d.paths["/items"].get.security = [];
    assert.equal(good(d).credentialRequired, false);
  }
  assert.equal(good({ ...doc(), security: [{}] }).credentialRequired, false);
  for (const security of [{}, null, [{}, {}], [{ auth: ["scope"] }]])
    bad({ ...doc(), security }, /array|Alternative|scopes/);
  bad(doc(op({ security: null })), /array/);
  bad(
    { ...doc(), security: [{ auth: [] }], components: { securitySchemes: {} } },
    /not found/,
  );
  for (const scheme of [
    { type: "apiKey", in: "query", name: "key" },
    { type: "apiKey", in: "cookie", name: "key" },
    { type: "http", scheme: "digest" },
    { type: "oauth2" },
    { type: "apiKey", in: "header", name: "bad\nheader" },
  ])
    bad(
      {
        ...doc(),
        security: [{ auth: [] }],
        components: { securitySchemes: { auth: scheme } },
      },
      /Only header/,
    );
  for (const name of [
    "Host",
    "Idempotency-Key",
    "Accept",
    "Content-Type",
    "Cookie",
  ])
    bad(
      {
        ...doc(),
        security: [{ auth: [] }],
        components: {
          securitySchemes: { auth: { type: "apiKey", in: "header", name } },
        },
      },
      /reserved headers/,
    );
  bad(
    {
      ...doc(),
      security: [{ a: [], b: [] }],
      components: {
        securitySchemes: {
          a: { type: "http", scheme: "basic" },
          b: { type: "http", scheme: "bearer" },
        },
      },
    },
    /conflicting/,
  );
});

test("paths block traversal, ambiguous encodings and variable expansion", () => {
  for (const path of [
    "relative",
    "//evil.example",
    "/item/{id}",
    "/items?q=x",
    "/x#frag",
    "/a b",
    "/a\\b",
  ])
    bad(doc(op(), "get", path), /static absolute/);
  for (const path of [
    "/a/../b",
    "/%2e%2e/b",
    "/a%2fb",
    "/%7bid%7d",
    "/%3fkey",
    "/%5cfoo",
    "/%20x",
  ])
    bad(doc(op(), "get", path), /Encoded separators/);
  bad(doc(op(), "get", "/%zz"), /Invalid path encoding/);
});

test("document size, depth, operation count and JSON precision fail before preview", () => {
  assert.equal(MAX_OPENAPI_BYTES, 1048576);
  for (const s of ["bad", "---\nopenapi: 3.0.3"])
    assert.throws(() => previewOpenAPI(s), /valid JSON/);
  for (const d of [null, [], true])
    assert.throws(() => preview(d), /OpenAPI object/);
  for (const openapi of ["2.0", "3.2.0", "3.0", 3])
    assert.throws(() => preview({ ...doc(), openapi }), /Only OpenAPI/);
  assert.throws(
    () => preview({ ...doc(), jsonSchemaDialect: "custom" }),
    /dialects/,
  );
  assert.throws(() => preview({ ...doc(), paths: null }), /OpenAPI object/);
  const base = JSON.stringify({ ...doc(), padding: "" });
  assert.equal(
    previewOpenAPI(
      base.replace(
        '"padding":""',
        '"padding":"' + "x".repeat(MAX_OPENAPI_BYTES - base.length) + '"',
      ),
    ).length,
    1,
  );
  assert.throws(
    () => previewOpenAPI(base + " ".repeat(MAX_OPENAPI_BYTES)),
    /1 MiB/,
  );
  assert.throws(
    () => preview({ ...doc(), padding: "中".repeat(400000) }),
    /1 MiB/,
  );
  assert.throws(() => previewOpenAPI('{"x":1,"x":2}'), /duplicate/);
  for (const k of ["__proto__", "constructor", "prototype"])
    assert.throws(() => previewOpenAPI(`{"${k}":{}}`), /unsafe property/);
  assert.equal(({} as any).polluted, undefined);
  assert.throws(() => previewOpenAPI('{"x":"\\u0000"}'), /null character/);
  for (const token of [
    "9007199254740993",
    "1e999",
    "1e-400",
    "1.0000000000000001",
    "0.1234567890123456789",
  ])
    assert.throws(
      () =>
        previewOpenAPI(
          JSON.stringify(doc()).replace('"3.0.3"', `"3.0.3","number":${token}`),
        ),
      /cannot preserve exactly/,
    );
  for (const token of ["0.1", "1e-1", "-0", "0e-400", "1.2500e2"])
    assert.equal(
      previewOpenAPI(
        JSON.stringify(doc()).replace('"3.0.3"', `"3.0.3","number":${token}`),
      ).length,
      1,
    );
  assert.throws(
    () => previewOpenAPI("[".repeat(49) + "0" + "]".repeat(49)),
    /nesting/,
  );
  assert.throws(
    () => preview({ ...doc(), padding: Array(60000).fill(0) }),
    /too many values/,
  );
  const paths = Object.fromEntries(
    Array.from({ length: 257 }, (_, i) => [
      "/p" + i,
      { get: op({ operationId: "op" + i }) },
    ]),
  );
  assert.throws(() => preview({ ...doc(), paths }), /256 operations/);
});

test("server bases cannot normalize away path segments and target length is bounded", () => {
  for (const server of [
    "https://example.com/a/../admin",
    "https://example.com/%2e%2e/admin",
    "https://example.com/%7bid%7d",
    "https://example.com/%00",
    "https://example.com/%252e%252e",
  ]) {
    bad(doc(), /Encoded separators/, server);
  }
  const prefix = "https://example.com/";
  const target = prefix + "a".repeat(4096 - prefix.length - "/items".length);
  assert.equal(good(doc(), target).url.length, 4096);
  bad(doc(), /4096 bytes/, target + "a");
  bad(doc(), /4096 bytes/, prefix + "中".repeat(1400));
  for (const path of ["/%00", "/%1f", "/%7f", "/%252f"])
    bad(doc(op(), "get", path), /Encoded separators/);
  bad(doc(op({ description: "a".repeat(4001) })), /4000 bytes/);
});

test("per-operation schema byte limit includes long parameter and property names", () => {
  const name = "a".repeat(40000);
  bad(
    doc(
      op({ parameters: [query({ type: "string" }, { name, required: true })] }),
    ),
    /64 KiB/,
  );
  bad(
    doc(
      body({
        type: "object",
        properties: {
          [name]: { type: "string" },
          ["b".repeat(40000)]: { type: "string" },
        },
      }),
      "post",
    ),
    /64 KiB/,
  );
});
