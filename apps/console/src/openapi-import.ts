/** Offline, conservative OpenAPI-to-HTTP mapping. No document URL is fetched. */
export const MAX_OPENAPI_BYTES = 1048576;
export type OpenAPIImportCandidate = {
  id: string;
  method: string;
  path: string;
  name: string;
  description: string;
  url: string;
  inputSchema: Record<string, unknown>;
  credentialRequired: boolean;
  warnings: string[];
};
export type OpenAPIImportOperation = {
  id: string;
  method: string;
  path: string;
  name: string;
  candidate?: OpenAPIImportCandidate;
  reason?: string;
};
type ObjectValue = Record<string, unknown>;
const methods = new Set(["get", "post", "put", "patch", "delete"]);
const operationMethods = new Set([...methods, "head", "options", "trace"]);
const enc = new TextEncoder();
function error(message: string): never {
  throw new Error(message);
}
const object = (value: unknown): value is ObjectValue =>
  !!value && typeof value === "object" && !Array.isArray(value);
function record(value: unknown): ObjectValue {
  if (!object(value)) error("Expected an OpenAPI object.");
  return value as ObjectValue;
}
const forbidden = new Set(["__proto__", "prototype", "constructor"]);

// JSON.parse silently accepts duplicate keys and rounded numeric constraints.
// Inspect its already-validated token stream before trusting the parsed document.
function parse(source: string): ObjectValue {
  if (enc.encode(source).length > MAX_OPENAPI_BYTES)
    error("OpenAPI document exceeds 1 MiB.");
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
  } catch {
    error("OpenAPI document must be valid JSON.");
  }
  const stack: (Set<string> | null)[] = [];
  let count = 0;
  const tokens =
    /"(?:[^"\\]|\\.)*"|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?|[{}\[\]:,]|true|false|null/g;
  for (const match of source.matchAll(tokens)) {
    const token = match[0];
    if (++count > 100000) error("OpenAPI document contains too many values.");
    if (token === "{" || token === "[") {
      stack.push(token === "{" ? new Set() : null);
      if (stack.length > 48)
        error("OpenAPI document exceeds the nesting limit.");
    } else if (token === "}" || token === "]") stack.pop();
    else if (token.startsWith('"')) {
      const text: string = JSON.parse(token);
      if (text.includes("\0"))
        error("OpenAPI document contains an invalid null character.");
      if (source.slice(match.index! + token.length).match(/^\s*:/)) {
        const keys = stack[stack.length - 1]!;
        if (forbidden.has(text))
          error("OpenAPI document contains an unsafe property name.");
        if (keys.has(text))
          error("OpenAPI document contains duplicate object keys.");
        keys.add(text);
      }
    } else if (/^[-\d]/.test(token)) {
      const value = Number(token);
      if (
        !Number.isFinite(value) ||
        (Number.isInteger(value) && !Number.isSafeInteger(value)) ||
        decimal(token) !== decimal(String(value))
      )
        error(
          "OpenAPI document contains a number the browser cannot preserve exactly.",
        );
    }
  }
  return record(parsed);
}
function decimal(token: string): string {
  const parts = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(token)!;
  const fraction = parts[3] ?? "";
  const digits = (parts[2] + fraction).replace(/^0+/, "");
  if (!digits) return "0";
  const significant = digits.replace(/0+$/, "");
  return `${parts[1]}${significant}e${BigInt(parts[4] ?? "0") - BigInt(fraction.length) + BigInt(digits.length - significant.length)}`;
}

const expansionError =
  "OpenAPI reference expansion exceeds the document budget.";
function resolver(document: ObjectValue) {
  let work = 0,
    bytes = 0;
  const charge = (size: number) => {
    work += 1;
    bytes += size;
    if (work > 32768 || bytes > 4 * MAX_OPENAPI_BYTES) error(expansionError);
  };
  const resolve = (
    value: unknown,
    parents: string[] = [],
  ): { value: ObjectValue; refs: string[] } => {
    charge(0);
    let current = record(value);
    const refs = [...parents];
    while (Object.hasOwn(current, "$ref")) {
      charge(0);
      const ref = current.$ref;
      if (typeof ref !== "string" || !ref.startsWith("#/"))
        error("Only local JSON Pointer references are supported.");
      if (Object.keys(current).length !== 1)
        error("References with sibling fields are not supported.");
      if (refs.includes(ref as string) || refs.length >= 24)
        error("Cyclic or deeply nested references are not supported.");
      refs.push(ref as string);
      let pointer: string;
      try {
        pointer = decodeURIComponent((ref as string).slice(2));
      } catch {
        return error("Invalid local reference.");
      }
      let target: unknown = document;
      for (const part of pointer.split("/")) {
        if (/~[^01]|~$/.test(part)) error("Invalid local reference.");
        const key = part.replace(/~1/g, "/").replace(/~0/g, "~");
        if (!object(target) || !Object.hasOwn(target, key))
          error("Local reference target was not found.");
        target = (target as ObjectValue)[key];
      }
      current = record(target);
    }
    return { value: current, refs };
  };
  return Object.assign(resolve, { charge });
}
type Resolve = ReturnType<typeof resolver>;
const schemaKeywords = new Set([
  "type",
  "properties",
  "required",
  "additionalProperties",
  "items",
  "enum",
  "const",
  "minimum",
  "maximum",
  "exclusiveMinimum",
  "exclusiveMaximum",
  "multipleOf",
  "minLength",
  "maxLength",
  "pattern",
  "minItems",
  "maxItems",
  "uniqueItems",
  "minProperties",
  "maxProperties",
  "title",
  "description",
  "default",
  "examples",
  "example",
  "nullable",
  "format",
  "readOnly",
  "writeOnly",
  "deprecated",
]);
const schemaTypes = new Set([
  "object",
  "array",
  "string",
  "number",
  "integer",
  "boolean",
  "null",
]);
const formats = new Set([
  "date",
  "date-time",
  "email",
  "hostname",
  "ipv4",
  "ipv6",
  "uri",
  "uuid",
]);
function chargeSchema(
  resolve: Resolve,
  budget: { bytes: number },
  size: number,
) {
  budget.bytes += size;
  resolve.charge(size);
  if (budget.bytes > 65536) error("Expanded input schema exceeds 64 KiB.");
}
function inputSchema(
  value: unknown,
  resolve: Resolve,
  version30: boolean,
  budget: { bytes: number },
): ObjectValue {
  let expanded = 0;
  function convert(raw: unknown, refs: string[] = [], depth = 0): ObjectValue {
    if (++expanded > 2048 || depth > 32)
      error("Expanded input schema exceeds the complexity limit.");
    const resolved = resolve(raw, refs),
      schema = resolved.value;
    if (Object.keys(schema).some((k) => !schemaKeywords.has(k)))
      error("Input schema uses unsupported keywords or a custom dialect.");
    // Count copied annotations and scalar constraints before allocating expanded
    // children. All parameters in an operation share this output-size budget.
    const own = Object.fromEntries(
      Object.entries(schema).filter(
        ([key]) =>
          !["properties", "items", "additionalProperties"].includes(key),
      ),
    );
    const size = enc.encode(JSON.stringify(own)).length;
    chargeSchema(resolve, budget, size);
    const type = schema.type;
    if (
      typeof type !== "string" ||
      !schemaTypes.has(type) ||
      (version30 && type === "null")
    )
      error("Input schema must declare a supported single type.");
    if (schema.readOnly === true || schema.writeOnly === true)
      error(
        "Read-only and write-only schema semantics require manual mapping.",
      );
    if (
      (schema.readOnly !== undefined && typeof schema.readOnly !== "boolean") ||
      (schema.writeOnly !== undefined && typeof schema.writeOnly !== "boolean")
    )
      error("Schema annotations have invalid types.");
    const result: ObjectValue = { ...schema };
    for (const annotation of ["title", "description", "format"])
      if (
        schema[annotation] !== undefined &&
        typeof schema[annotation] !== "string"
      )
        error("Schema annotations have invalid types.");
    if (schema.format !== undefined && !formats.has(schema.format as string))
      error("Input schema format requires manual validation.");
    if (schema.format !== undefined && type !== "string")
      error("Input schema format requires manual validation.");
    if (schema.nullable !== undefined) {
      if (!version30 || typeof schema.nullable !== "boolean")
        error("Unsupported nullable schema syntax.");
      if (schema.nullable) result.type = [type, "null"];
      delete result.nullable;
    }
    for (const [exclusive, bound] of [
      ["exclusiveMinimum", "minimum"],
      ["exclusiveMaximum", "maximum"],
    ]) {
      if (schema[exclusive] === undefined) continue;
      if (version30) {
        if (
          typeof schema[exclusive] !== "boolean" ||
          (schema[exclusive] && typeof schema[bound] !== "number")
        )
          error("Invalid OpenAPI 3.0 exclusive bound.");
        delete result[exclusive];
        if (schema[exclusive]) {
          result[exclusive] = schema[bound];
          delete result[bound];
        }
      } else if (typeof schema[exclusive] !== "number")
        error("Invalid JSON Schema exclusive bound.");
    }
    for (const keyword of [
      "minimum",
      "maximum",
      "multipleOf",
      "minLength",
      "maxLength",
      "minItems",
      "maxItems",
      "minProperties",
      "maxProperties",
    ]) {
      const v = schema[keyword];
      if (
        v !== undefined &&
        (typeof v !== "number" ||
          (keyword === "multipleOf"
            ? v <= 0
            : (keyword.startsWith("min") && keyword !== "minimum") ||
                (keyword.startsWith("max") && keyword !== "maximum")
              ? !Number.isInteger(v) || v < 0
              : false))
      )
        error("Input schema has an invalid numeric constraint.");
    }
    if (schema.pattern !== undefined) {
      if (typeof schema.pattern !== "string")
        error("Input schema has an invalid pattern.");
      // Go's JSON Schema regex engine differs from JavaScript for these features.
      if (
        /\\[A-Za-z0-9]|\(\?/.test(schema.pattern as string) ||
        (schema.pattern.includes("(") && schema.pattern.includes("{")) ||
        [...(schema.pattern as string).matchAll(/\{(\d+)(?:,(\d*))?\}/g)].some(
          (match) => Number(match[1]) > 1000 || Number(match[2] ?? 0) > 1000,
        )
      )
        error("Input schema pattern requires manual validation.");
      try {
        new RegExp(schema.pattern as string);
      } catch {
        error("Input schema has an invalid pattern.");
      }
    }
    if (
      schema.enum !== undefined &&
      (!Array.isArray(schema.enum) ||
        !schema.enum.length ||
        new Set(schema.enum.map((v) => JSON.stringify(v))).size !==
          schema.enum.length)
    )
      error("Input schema has an invalid enum.");
    if (
      (schema.uniqueItems !== undefined &&
        typeof schema.uniqueItems !== "boolean") ||
      (schema.deprecated !== undefined &&
        typeof schema.deprecated !== "boolean") ||
      (schema.examples !== undefined && !Array.isArray(schema.examples))
    )
      error("Schema annotations have invalid types.");
    if (schema.properties !== undefined) {
      if (type !== "object")
        error("Object constraints require an object schema.");
      result.properties = Object.fromEntries(
        Object.entries(record(schema.properties)).map(([key, child]) => {
          chargeSchema(
            resolve,
            budget,
            enc.encode(JSON.stringify(key)).length + 2,
          );
          return [key, convert(child, resolved.refs, depth + 1)];
        }),
      );
    }
    if (schema.required !== undefined) {
      if (
        type !== "object" ||
        !Array.isArray(schema.required) ||
        schema.required.some((k) => typeof k !== "string") ||
        new Set(schema.required).size !== schema.required.length
      )
        error("Input schema has invalid required properties.");
    }
    if (schema.additionalProperties !== undefined) {
      if (type !== "object")
        error("Object constraints require an object schema.");
      result.additionalProperties =
        typeof schema.additionalProperties === "boolean"
          ? schema.additionalProperties
          : convert(schema.additionalProperties, resolved.refs, depth + 1);
    }
    if (schema.items !== undefined) {
      if (type !== "array") error("Array constraints require an array schema.");
      result.items = convert(schema.items, resolved.refs, depth + 1);
    }
    return result;
  }
  const result = convert(value);
  if (enc.encode(JSON.stringify(result)).length > 65536)
    error("Expanded input schema exceeds 64 KiB.");
  return result;
}

function serverURL(server: unknown, path: string): string {
  if (
    typeof server !== "string" ||
    !/^https?:\/\//.test(server) ||
    /[\s\\{}?#]/.test(server)
  )
    error(
      "Select an absolute HTTP(S) server URL without credentials, query, fragment or variables.",
    );
  if (
    (server as string).length + path.length > 4096 ||
    enc.encode((server as string) + path).length > 4096
  )
    error("Server URL and operation path must fit within 4096 bytes.");
  let url: URL;
  try {
    url = new URL(server as string);
  } catch {
    return error("Invalid server URL.");
  }
  if (url.username || url.password)
    error("Server URLs must not contain credentials.");
  checkPath((server as string).replace(/^https?:\/\/[^/]*/, "") || "/");
  // Concatenation retains the OpenAPI server base path instead of replacing it.
  return url.toString().replace(/\/$/, "") + path;
}
function checkPath(path: string) {
  if (!path.startsWith("/") || path.startsWith("//") || /[{}?#\\\s]/.test(path))
    error(
      "Only static absolute paths without parameters, query or fragment are supported.",
    );
  let decoded: string;
  try {
    decoded = decodeURIComponent(path);
  } catch {
    return error("Invalid path encoding.");
  }
  if (
    /[{}?#%\\\s\x00-\x1f\x7f]/.test(decoded) ||
    decoded.split("/").some((p) => p === "." || p === "..") ||
    /%2f/i.test(path)
  )
    error(
      "Encoded separators, dot segments and dynamic paths require manual mapping.",
    );
}
function securityRequired(
  security: unknown,
  doc: ObjectValue,
  resolve: Resolve,
): boolean {
  if (security === undefined) return false;
  if (!Array.isArray(security))
    error("Security requirements must be an array.");
  if (security.length === 0) return false;
  if (security.length !== 1)
    error("Alternative security requirements require manual mapping.");
  const required = record(security[0]);
  if (Object.keys(required).length > 16)
    error("Authentication requires conflicting or reserved headers.");
  const headers = new Set<string>();
  for (const [name, scopes] of Object.entries(required)) {
    if (!Array.isArray(scopes) || scopes.length)
      error("Security scopes require manual mapping.");
    const components = record(doc.components),
      schemes = record(components.securitySchemes);
    if (!Object.hasOwn(schemes, name)) error("Security scheme was not found.");
    const scheme = resolve(schemes[name]).value;
    let header: string;
    if (
      scheme.type === "http" &&
      (scheme.scheme === "bearer" || scheme.scheme === "basic")
    )
      header = "authorization";
    else if (
      scheme.type === "apiKey" &&
      scheme.in === "header" &&
      typeof scheme.name === "string" &&
      scheme.name.length <= 128 &&
      /^[!#$%&'*+.^_`|~\w-]+$/.test(scheme.name)
    )
      header = scheme.name.toLowerCase();
    else
      return error(
        "Only header API keys and HTTP basic or bearer authentication can be mapped.",
      );
    if (
      [
        "host",
        "content-length",
        "transfer-encoding",
        "connection",
        "idempotency-key",
        "accept",
        "content-type",
        "cookie",
        "proxy-authorization",
        "proxy-connection",
        "te",
        "trailer",
        "upgrade",
      ].includes(header) ||
      header.startsWith("mcp-") ||
      headers.has(header)
    )
      error("Authentication requires conflicting or reserved headers.");
    headers.add(header);
  }
  return headers.size > 0;
}
function jsonSuccess(responses: unknown, resolve: Resolve) {
  const entries = Object.entries(record(responses)).filter(
    ([code]) => /^2\d\d$/.test(code) || /^2XX$/i.test(code),
  );
  if (!entries.length) error("A documented JSON success response is required.");
  for (const [code, raw] of entries) {
    if (code === "204" || code === "205" || /^2XX$/i.test(code))
      error("Empty or wildcard success responses require manual mapping.");
    const response = resolve(raw).value;
    const content = record(response.content);
    if (
      Object.keys(content).length !== 1 ||
      !object(content["application/json"])
    )
      error(
        "Every success response must declare only application/json content.",
      );
  }
}
function parameters(raw: unknown, resolve: Resolve): ObjectValue[] {
  if (raw === undefined) return [];
  if (!Array.isArray(raw) || raw.length > 128)
    error("Parameters must be a bounded array.");
  const seen = new Set<string>();
  return raw.map((item) => {
    const p = resolve(item).value;
    if (
      typeof p.name !== "string" ||
      !p.name ||
      forbidden.has(p.name) ||
      typeof p.in !== "string"
    )
      error("Invalid parameter name or location.");
    const key = `${p.in}:${p.name}`;
    if (seen.has(key)) error("Duplicate parameters require manual review.");
    seen.add(key);
    return p;
  });
}
function candidate(
  doc: ObjectValue,
  pathItem: ObjectValue,
  op: ObjectValue,
  row: OpenAPIImportOperation,
  override: string | undefined,
  resolve: Resolve,
  version30: boolean,
): OpenAPIImportCandidate {
  if (!methods.has(row.method.toLowerCase()))
    error("This HTTP method is not supported by the gateway adapter.");
  checkPath(row.path);
  if (op.callbacks !== undefined || op.webhooks !== undefined)
    error("Callbacks and webhooks require manual mapping.");
  const servers =
    op.servers !== undefined
      ? op.servers
      : pathItem.servers !== undefined
        ? pathItem.servers
        : doc.servers;
  let server: unknown = override;
  const warnings = [
    "Output schemas are not imported. Review the response contract before publishing.",
    "Imported tools are write-risk drafts requiring approval. Review the risk and permissions before publishing.",
  ];
  if (override === undefined) {
    if (!Array.isArray(servers) || !servers.length)
      error("Select an absolute HTTP(S) server URL.");
    server = record(servers[0]).url;
    if (servers.length > 1)
      warnings.push(
        "The first declared server was selected. Review the destination before importing.",
      );
  }
  const url = serverURL(server, row.path);
  const credentialRequired = securityRequired(
    op.security !== undefined ? op.security : doc.security,
    doc,
    resolve,
  );
  if (credentialRequired)
    warnings.push(
      "Configure an operator-managed credential for the exact destination origin. No authentication secrets are imported.",
    );
  jsonSuccess(op.responses, resolve);
  const inherited = parameters(pathItem.parameters, resolve),
    own = parameters(op.parameters, resolve);
  const merged = new Map(inherited.map((p) => [`${p.in}:${p.name}`, p]));
  for (const p of own) merged.set(`${p.in}:${p.name}`, p);
  let schema: ObjectValue;
  const budget = { bytes: 0 };
  if (row.method === "GET") {
    if (op.requestBody !== undefined)
      error("GET request bodies are not supported.");
    const properties: ObjectValue = Object.create(null),
      required: string[] = [];
    for (const p of merged.values()) {
      if (
        p.in !== "query" ||
        p.content !== undefined ||
        (p.style !== undefined && p.style !== "form") ||
        (p.explode !== undefined && typeof p.explode !== "boolean") ||
        (p.allowReserved !== undefined && p.allowReserved !== false) ||
        (p.allowEmptyValue !== undefined && p.allowEmptyValue !== false)
      )
        error(
          "Only scalar query parameters with form serialization are supported.",
        );
      if (p.required !== undefined && typeof p.required !== "boolean")
        error("Parameter required must be a boolean.");
      chargeSchema(
        resolve,
        budget,
        enc.encode(JSON.stringify(p.name)).length * (p.required ? 2 : 1) + 2,
      );
      const converted = inputSchema(p.schema, resolve, version30, budget);
      if (
        !["string", "number", "integer", "boolean"].includes(
          converted.type as string,
        )
      )
        error(
          "Query arrays, objects and nullable values require manual mapping.",
        );
      properties[p.name as string] = converted;
      if (p.required) required.push(p.name as string);
    }
    schema = {
      type: "object",
      properties,
      required,
      additionalProperties: false,
    };
  } else {
    if (merged.size)
      error(
        "Body requests cannot include query, path, header or cookie parameters.",
      );
    if (op.requestBody === undefined)
      error(
        "This adapter always sends a JSON object body for non-GET requests.",
      );
    const body = resolve(op.requestBody).value;
    if (body.required !== true)
      error(
        "Optional request bodies require manual mapping because this adapter always sends a body.",
      );
    const content = record(body.content);
    if (
      Object.keys(content).length !== 1 ||
      !object(content["application/json"])
    )
      error("Request bodies must declare only application/json content.");
    const media = content["application/json"] as ObjectValue;
    if (media.encoding !== undefined)
      error("Custom body encoding requires manual mapping.");
    schema = inputSchema(media.schema, resolve, version30, budget);
    if (schema.type !== "object")
      error("Request body schema must require a non-null JSON object.");
  }
  if (enc.encode(JSON.stringify(schema)).length > 65536)
    error("Expanded input schema exceeds 64 KiB.");
  warnings.push(
    "Schema defaults are annotations and are not injected into requests. Gateway egress policy still applies.",
  );
  if (
    [op.summary, op.description].some(
      (value) => typeof value === "string" && value.length > 4000,
    )
  )
    error("Operation description exceeds 4000 bytes.");
  const description =
    [op.summary, op.description]
      .filter((v) => typeof v === "string" && v.trim())
      .join("\n\n") || `${row.method} ${row.path}`;
  if (enc.encode(description).length > 4000)
    error("Operation description exceeds 4000 bytes.");
  return {
    ...row,
    url,
    description,
    inputSchema: schema,
    credentialRequired,
    warnings,
  };
}

/** Unsupported operations are shown with a reason; malformed documents throw fixed errors. */
export function previewOpenAPI(
  source: string,
  serverOverride?: string,
): OpenAPIImportOperation[] {
  const doc = parse(source);
  if (typeof doc.openapi !== "string" || !/^3\.[01]\.\d+$/.test(doc.openapi))
    error("Only OpenAPI 3.0.x and 3.1.x JSON documents are supported.");
  if (doc.jsonSchemaDialect !== undefined)
    error("Custom OpenAPI schema dialects are not supported.");
  const paths = record(doc.paths),
    resolve = resolver(doc),
    rows: OpenAPIImportOperation[] = [];
  for (const [path, raw] of Object.entries(paths)) {
    if (path.startsWith("x-")) continue;
    let pathItem: ObjectValue;
    try {
      pathItem = resolve(raw).value;
    } catch (failure) {
      if ((failure as Error).message === expansionError) throw failure;
      error(
        "Path item references must resolve to local objects without cycles or siblings.",
      );
    }
    for (const [method, rawOperation] of Object.entries(pathItem!)) {
      if (!operationMethods.has(method)) continue;
      if (rows.length >= 256) error("OpenAPI document exceeds 256 operations.");
      const id = `${method.toUpperCase()} ${path}`;
      const name = `api_${method}_${rows.length + 1}`;
      const row: OpenAPIImportOperation = {
        id,
        method: method.toUpperCase(),
        path,
        name,
      };
      try {
        const op = record(rawOperation);
        if (
          typeof op.operationId === "string" &&
          /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/.test(op.operationId)
        )
          row.name = op.operationId;
        row.candidate = candidate(
          doc,
          pathItem!,
          op,
          row,
          serverOverride,
          resolve,
          (doc.openapi as string).startsWith("3.0."),
        );
      } catch (failure) {
        if ((failure as Error).message === expansionError) throw failure;
        row.reason = (failure as Error).message;
      }
      rows.push(row);
    }
  }
  // Duplicate operationIds cannot safely become tool names, even on different paths.
  const counts = new Map<string, number>();
  for (const row of rows) counts.set(row.name, (counts.get(row.name) ?? 0) + 1);
  for (const row of rows)
    if (counts.get(row.name)! > 1) {
      delete row.candidate;
      row.reason = "Duplicate generated tool names require manual review.";
    }
  return rows;
}
