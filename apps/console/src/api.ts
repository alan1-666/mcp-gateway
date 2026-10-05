export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

const reloadConflictMessages = new Set([
  "resource conflict",
  "resource conflict: client was changed; reload before editing",
  "resource conflict: client was changed; reload before rotating",
  "resource conflict: credential changed; reload before retrying",
  "resource conflict: reconciliation changed; reload before adding evidence",
  "resource conflict: capacity limits changed; reload before saving",
  "resource conflict: tool changed while this candidate was under review",
]);

export function requiresConflictReload(error: unknown): boolean {
  // The current gateway uses one conflict code for both version checks and
  // business constraints. Keep this list explicit: upstream contracts changing
  // is a business failure, not evidence that the local record version changed.
  // A bare legacy conflict is ambiguous, so require a read before another write.
  return (
    error instanceof APIError &&
    error.status === 409 &&
    error.code === "conflict" &&
    reloadConflictMessages.has(error.message.trim())
  );
}

export class APIClient {
  private csrf = "";
  constructor(private readonly token = "") {}
  setCSRF(value: string) {
    this.csrf = value;
  }

  async request<T>(
    path: string,
    options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
  ): Promise<T> {
    const timeout = AbortSignal.timeout(
      options.method && options.method !== "GET" ? 135000 : 20000,
    );
    const signal = options.signal
      ? AbortSignal.any([options.signal, timeout])
      : timeout;
    const response = await fetch(`/api/v1${path}`, {
      method: options.method ?? "GET",
      headers: {
        Accept: "application/json",
        ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
        ...(this.csrf ? { "X-CSRF-Token": this.csrf } : {}),
        ...(options.body !== undefined
          ? { "Content-Type": "application/json" }
          : {}),
      },
      ...(options.body !== undefined
        ? { body: JSON.stringify(options.body) }
        : {}),
      signal,
      credentials: this.token ? "omit" : "same-origin",
      cache: "no-store",
      redirect: "error",
    });
    const contentType = response.headers.get("content-type") ?? "";
    if (!contentType.includes("application/json")) {
      throw new APIError(
        "The gateway returned an unexpected response. Check the API connection.",
        response.status,
        "invalid_response",
      );
    }
    const data: unknown = await response.json();
    if (!response.ok) {
      const error =
        data && typeof data === "object" && "error" in data ? data.error : null;
      const message =
        error &&
        typeof error === "object" &&
        "message" in error &&
        typeof error.message === "string"
          ? error.message
          : `Request failed (${response.status}).`;
      const code =
        error &&
        typeof error === "object" &&
        "code" in error &&
        typeof error.code === "string"
          ? error.code
          : "request_failed";
      throw new APIError(message, response.status, code);
    }
    return data as T;
  }
}

export function messageOf(error: unknown): string {
  if (error instanceof APIError)
    return error.status === 429
      ? "Workspace capacity or request rate has been reached. Wait for active work to finish, then try again."
      : error.message;
  if (error instanceof DOMException && error.name === "TimeoutError")
    return "The gateway did not respond in time. Refresh the operation record to check its current state.";
  if (error instanceof TypeError)
    return "Unable to reach the gateway. Check the connection and try again.";
  return error instanceof Error
    ? error.message
    : "The request could not be completed.";
}

export { parseObject } from "./json";
