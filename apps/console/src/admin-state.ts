import { APIError, messageOf, requiresConflictReload } from "./api";
import type { APIClient } from "./api";

export interface ActionState {
  busy: boolean;
  error: string;
  needsReload: boolean;
}
export class AdminAction {
  private state: ActionState = { busy: false, error: "", needsReload: false };
  private generation = 0;
  private listeners = new Set<() => void>();
  constructor(private readonly api: Pick<APIClient, "request">) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private set(state: ActionState) {
    this.state = state;
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.generation++;
  }
  reset = () => {
    if (!this.state.busy)
      this.set({ busy: false, error: "", needsReload: false });
  };
  async run<T>(path: string, body: unknown): Promise<T | null> {
    if (this.state.busy || this.state.needsReload) return null;
    const generation = this.generation;
    this.set({ busy: true, error: "", needsReload: false });
    try {
      const result = await this.api.request<T>(path, { method: "POST", body });
      if (generation !== this.generation) return null;
      this.set({ busy: false, error: "", needsReload: false });
      return result;
    } catch (error) {
      if (generation !== this.generation) return null;
      const conflict = requiresConflictReload(error);
      const uncertain = !(error instanceof APIError) || error.status >= 500;
      this.set({
        busy: false,
        needsReload: conflict || uncertain,
        error:
          messageOf(error) +
          (conflict
            ? " Reload current data and review your inputs before submitting again."
            : uncertain
              ? " The request may have completed. Reload before making another change."
              : ""),
      });
      return null;
    }
  }
}

export function toggleGrant(values: string[], id: string): string[] {
  return values.includes(id)
    ? values.filter((value) => value !== id)
    : [...values, id];
}
export function clientScopes(read: boolean, invoke: boolean) {
  return read ? ["tools:read", ...(invoke ? ["tools:invoke"] : [])] : [];
}
export function isoDate(value: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime()))
    throw new Error("Enter a valid date and time.");
  return date.toISOString();
}
export function filterPath(
  path: string,
  filters: Record<string, string>,
): string {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(filters)) {
    const trimmed = value.trim();
    if (trimmed) params.set(name, trimmed);
  }
  return `${path}${params.size ? `?${params}` : ""}`;
}

export function credentialHeaders(
  rows: { name: string; value: string }[],
): Record<string, string> {
  const headers: Record<string, string> = {};
  const seen = new Set<string>();
  for (const row of rows) {
    const name = row.name.trim(),
      lower = name.toLowerCase();
    if (!name || !row.value)
      throw new Error("Each header needs a name and secret value.");
    if (seen.has(lower))
      throw new Error("Header names must be unique, ignoring letter case.");
    seen.add(lower);
    headers[name] = row.value;
  }
  if (!rows.length || rows.length > 16)
    throw new Error("Provide between 1 and 16 authentication headers.");
  return headers;
}
