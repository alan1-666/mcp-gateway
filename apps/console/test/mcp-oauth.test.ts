import test from "node:test";
import assert from "node:assert/strict";
import { APIError } from "../src/api.ts";
import {
  cleanOAuthCallbackURL,
  MCPOAuthController,
  oauthCallback,
  parseOAuthConfiguration,
} from "../src/mcp-oauth.ts";
import type { OAuthMetadata, OAuthStatus } from "../src/mcp-oauth.ts";

const configuration = {
  issuer: "https://identity.example",
  client_id: "registered-client",
  auth_method: "none" as const,
  scopes: ["tools:read"],
};
const saved: OAuthStatus = {
  server_id: "docs/server",
  status: "disconnected",
  version: 3,
  configuration,
  redirect_uri: "https://gateway.example/api/v1/mcp/oauth/callback",
};
const metadata: OAuthMetadata = {
  issuer: configuration.issuer,
  issuers: [configuration.issuer, "https://other.example"],
  resource: "https://docs.example/mcp",
  authorization_endpoint: "https://identity.example/authorize",
  token_endpoint: "https://identity.example/token",
  scopes_supported: ["tools:read", "tools:write"],
  auth_methods: ["none", "client_secret_basic"],
  redirect_uri: saved.redirect_uri,
};
const draft = {
  clientID: configuration.client_id,
  authMethod: configuration.auth_method,
  scopes: "tools:read",
};
type Options = { method?: string; body?: unknown; signal?: AbortSignal };
function client(
  request: (path: string, options?: Options) => Promise<unknown>,
) {
  return {
    async request<T>(path: string, options?: Options): Promise<T> {
      return (await request(path, options)) as T;
    },
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
function authorizationURL() {
  const url = new URL(metadata.authorization_endpoint);
  url.search = new URLSearchParams({
    response_type: "code",
    client_id: configuration.client_id,
    redirect_uri: saved.redirect_uri,
    state: "opaque-state",
    code_challenge_method: "S256",
    code_challenge: "challenge",
    resource: metadata.resource,
  }).toString();
  return url.href;
}

test("OAuth configuration validates methods and scopes and includes secrets only for confidential clients", () => {
  assert.deepEqual(
    parseOAuthConfiguration(
      {
        ...draft,
        clientID: " registered-client ",
        scopes: "tools:read  tools:write\ntools:read",
      },
      metadata,
      "ignored-secret",
    ),
    { ...configuration, scopes: ["tools:read", "tools:write"] },
  );
  assert.deepEqual(
    parseOAuthConfiguration(
      { ...draft, authMethod: "client_secret_basic" },
      metadata,
      " private secret ",
    ),
    {
      ...configuration,
      auth_method: "client_secret_basic",
      client_secret: " private secret ",
    },
  );
  assert.throws(
    () =>
      parseOAuthConfiguration(
        { ...draft, authMethod: "client_secret_basic" },
        metadata,
        "",
      ),
    /client secret/,
  );
  assert.throws(
    () => parseOAuthConfiguration({ ...draft, clientID: "" }, metadata, ""),
    /client ID/,
  );
  assert.throws(
    () =>
      parseOAuthConfiguration(
        draft,
        { ...metadata, auth_methods: ["client_secret_basic"] },
        "",
      ),
    /supported/,
  );
  for (const scopes of [
    'read"all',
    "read\\all",
    "权限",
    Array.from({ length: 65 }, (_, i) => `scope${i}`).join(" "),
  ])
    assert.throws(
      () => parseOAuthConfiguration({ ...draft, scopes }, metadata, ""),
      /scopes/,
    );
  assert.deepEqual(
    parseOAuthConfiguration({ ...draft, scopes: "" }, metadata, "").scopes,
    [],
  );
});

test("OAuth callback uses fixed outcomes and removes sensitive query fields while preserving routing", () => {
  assert.equal(
    oauthCallback("?oauth=connected&error_description=unsafe"),
    "connected",
  );
  assert.equal(
    oauthCallback("?oauth=reconnect_required"),
    "reconnect_required",
  );
  for (const search of [
    "?oauth=<script>",
    "?oauth=connected&oauth=reconnect_required",
    "?error=denied",
    "",
  ])
    assert.equal(oauthCallback(search), null);
  assert.equal(
    cleanOAuthCallbackURL(
      "https://gateway.example/console/?oauth=connected&code=secret&state=state&iss=issuer&error=denied&error_description=unsafe&view=mcp#settings",
    ),
    "/console/?view=mcp#settings",
  );
});

test("OAuth read ignores cancelled stale responses even when the transport ignores abort", async () => {
  const first = deferred<OAuthStatus>();
  let requests = 0;
  let signal: AbortSignal | undefined;
  const controller = new MCPOAuthController(
    client(async (_, options) => {
      if (++requests === 1) {
        signal = options?.signal;
        return first.promise;
      }
      return { ...saved, version: 4, status: "connected" };
    }),
    saved.server_id,
  );
  const stale = controller.load();
  controller.cancel();
  await controller.load();
  first.resolve(saved);
  await stale;
  assert.equal(signal?.aborted, true);
  assert.equal(controller.getSnapshot().saved?.version, 4);
  assert.equal(controller.getSnapshot().saved?.status, "connected");
});

test("OAuth mutation uses the read version, rejects double submit, and never stores a client secret", async () => {
  const update = deferred<OAuthStatus>();
  const writes: Options[] = [];
  const controller = new MCPOAuthController(
    client(async (path, options) => {
      if (path.endsWith("/discover")) return metadata;
      if (options?.method === "PUT") {
        writes.push(options);
        return update.promise;
      }
      return saved;
    }),
    saved.server_id,
  );
  await controller.load();
  await controller.discover();
  const saving = controller.save(
    { ...draft, authMethod: "client_secret_basic" },
    "private-secret",
  );
  assert.equal(await controller.save(draft, ""), null);
  assert.equal(await controller.disconnect(), null);
  assert.equal(await controller.load(), null);
  assert.equal(writes.length, 1);
  assert.deepEqual(writes[0].body, {
    ...configuration,
    auth_method: "client_secret_basic",
    expected_version: 3,
    client_secret: "private-secret",
  });
  update.resolve({
    ...saved,
    version: 4,
    configuration: {
      ...configuration,
      auth_method: "client_secret_basic",
      client_secret: "unexpected-secret",
    },
  } as OAuthStatus);
  await saving;
  assert.equal(controller.getSnapshot().saved?.version, 4);
  assert.equal(
    JSON.stringify(controller.getSnapshot()).includes("private-secret"),
    false,
  );
  assert.equal(
    JSON.stringify(controller.getSnapshot()).includes("unexpected-secret"),
    false,
  );
});

test("a conflict or ambiguous mutation failure requires a fresh status before any retry", async () => {
  for (const failure of [
    new APIError("OAuth version changed", 409, "conflict"),
    new TypeError("network interrupted"),
  ]) {
    let writes = 0;
    let reads = 0;
    const controller = new MCPOAuthController(
      client(async (_, options) => {
        if (options?.method === "POST") {
          writes++;
          if (writes === 1) throw failure;
          return { ...saved, version: 5 };
        }
        return { ...saved, version: reads++ === 0 ? 3 : 4 };
      }),
      saved.server_id,
    );
    await controller.load();
    await controller.disconnect();
    assert.equal(controller.getSnapshot().needsReload, true);
    await controller.disconnect();
    assert.equal(writes, 1);
    await controller.load();
    await controller.disconnect();
    assert.equal(writes, 2);
    assert.equal(controller.getSnapshot().saved?.version, 5);
  }
});

test("changing providers discards stale metadata and requires successful rediscovery", async () => {
  const controller = new MCPOAuthController(
    client(async (path, options) => {
      if (!path.endsWith("/discover")) return saved;
      if ((options?.body as { issuer?: string }).issuer)
        throw new Error("provider unavailable");
      return metadata;
    }),
    saved.server_id,
  );
  await controller.load();
  await controller.discover();
  assert.equal(controller.getSnapshot().metadata?.issuer, metadata.issuer);
  await controller.discover("https://other.example");
  assert.equal(controller.getSnapshot().metadata, null);
  assert.equal(await controller.save(draft, ""), null);
});

test("only a PKCE authorization link for the configured client and callback may navigate", async () => {
  const valid = authorizationURL();
  const invalid = [
    "javascript:alert(1)",
    valid.replace("https:", "http:"),
    valid.replace("S256", "plain"),
    valid.replace("registered-client", "different-client"),
    valid.replace("gateway.example", "attacker.example"),
  ];
  for (const url of [valid, ...invalid]) {
    let attempts = 0;
    const controller = new MCPOAuthController(
      client(async (path) => {
        if (path.endsWith("/connect")) {
          attempts++;
          return { authorization_url: url, expires_at: "2026-10-06T14:00:00Z" };
        }
        return saved;
      }),
      saved.server_id,
    );
    await controller.load();
    assert.equal(await controller.connect(), url === valid ? valid : null);
    assert.equal(controller.getSnapshot().needsReload, true);
    await controller.connect();
    assert.equal(attempts, 1);
  }
});

test("cancelled mutations cannot trigger navigation or restore a discarded connection", async () => {
  const response = deferred<unknown>();
  const controller = new MCPOAuthController(
    client(async (path) =>
      path.endsWith("/connect") ? response.promise : saved,
    ),
    saved.server_id,
  );
  await controller.load();
  const connect = controller.connect();
  controller.cancel();
  response.resolve({
    authorization_url: authorizationURL(),
    expires_at: "2026-10-06T14:00:00Z",
  });
  assert.equal(await connect, null);
  assert.equal(controller.getSnapshot().needsReload, true);
});

test("permission loss clears cached configuration and malformed status cannot unlock actions", async () => {
  let authorized = true;
  const controller = new MCPOAuthController(
    client(async () => {
      if (!authorized) throw new APIError("forbidden", 403, "forbidden");
      return saved;
    }),
    saved.server_id,
  );
  await controller.load();
  authorized = false;
  await controller.load();
  assert.equal(controller.getSnapshot().saved, null);
  assert.equal(controller.getSnapshot().metadata, null);
  for (const value of [
    { ...saved, server_id: "another-server" },
    { ...saved, version: -1 },
    { ...saved, redirect_uri: "http://gateway.example/callback" },
    { ...saved, status: "unknown" },
  ]) {
    const invalid = new MCPOAuthController(
      client(async () => value),
      saved.server_id,
    );
    await invalid.load();
    assert.equal(invalid.getSnapshot().saved, null);
    assert.equal(invalid.getSnapshot().needsReload, true);
  }
});
