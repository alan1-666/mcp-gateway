import { APIError, messageOf } from "./api";
import type { ToolPage } from "./types";

export type ToolSearchScope = "registry" | "discovery";

export function normalizeToolQuery(input: string): string {
  const query = input.trim();
  if (new TextEncoder().encode(query).length > 200) {
    throw new Error(
      "Search is limited to 200 UTF-8 bytes. Shorten the name or description.",
    );
  }
  return query;
}

export function toolSearchPath(
  scope: ToolSearchScope,
  input: string,
  cursor = "",
  limit = scope === "registry" ? 50 : 25,
): string {
  const query = normalizeToolQuery(input);
  if (!Number.isInteger(limit) || limit < 1 || limit > 50)
    throw new Error("Page size must be an integer between 1 and 50.");
  if (new TextEncoder().encode(cursor).length > 2048)
    throw new Error(
      "The gateway returned an invalid search cursor. Restart the search.",
    );
  const params = new URLSearchParams({ limit: String(limit) });
  if (query) params.set("query", query);
  if (cursor) params.set("cursor", cursor);
  return `${scope === "registry" ? "/tools" : "/catalog/tools"}?${params}`;
}

export function validateToolPage<T extends { id: string }>(
  page: ToolPage<T>,
  cursor = "",
): ToolPage<T> {
  if (
    !page ||
    !Array.isArray(page.items) ||
    !Number.isSafeInteger(page.total) ||
    page.total < 0 ||
    (page.next_cursor !== undefined && typeof page.next_cursor !== "string") ||
    page.items.some((item) => !item || typeof item.id !== "string" || !item.id)
  ) {
    throw new Error(
      "The gateway returned an invalid tool page. Retry the search.",
    );
  }
  if (page.next_cursor && page.next_cursor === cursor)
    throw new Error("The search cursor did not advance. Restart the search.");
  return page;
}

export interface ToolSearchState<T> {
  input: string;
  query: string;
  items: T[];
  total: number | null;
  nextCursor: string;
  phase: "idle" | "loading" | "loading-more";
  loaded: boolean;
  error: string;
}

type PageLoader<T> = (
  query: string,
  cursor: string,
  signal: AbortSignal,
) => Promise<ToolPage<T>>;

// A request generation owns its query and cursor. Abort saves work; generation
// checks also reject old results when a transport ignores cancellation.
export class ToolSearchController<T extends { id: string }> {
  private state: ToolSearchState<T> = {
    input: "",
    query: "",
    items: [],
    total: null,
    nextCursor: "",
    phase: "idle",
    loaded: false,
    error: "",
  };
  private generation = 0;
  private request: AbortController | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private retryCursor: string | null = null;
  private listeners = new Set<() => void>();

  constructor(private readonly loader: PageLoader<T>) {}

  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private publish(next: ToolSearchState<T>) {
    this.state = next;
    for (const listener of this.listeners) listener();
  }

  cancel() {
    this.generation += 1;
    this.request?.abort();
    this.request = null;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }

  setQuery(input: string, delay = 0): Promise<void> {
    this.cancel();
    this.retryCursor = null;
    let query: string;
    try {
      query = normalizeToolQuery(input);
    } catch (error) {
      this.publish({
        input,
        query: "",
        items: [],
        total: null,
        nextCursor: "",
        phase: "idle",
        loaded: false,
        error: messageOf(error),
      });
      return Promise.resolve();
    }
    this.publish({
      input,
      query,
      items: [],
      total: null,
      nextCursor: "",
      phase: "loading",
      loaded: false,
      error: "",
    });
    const generation = this.generation;
    if (delay > 0) {
      this.timer = setTimeout(() => {
        this.timer = null;
        void this.fetchPage("", generation);
      }, delay);
      return Promise.resolve();
    }
    return this.fetchPage("", generation);
  }

  reload = () => this.setQuery(this.state.input);

  loadMore = (): Promise<void> => {
    if (this.state.phase !== "idle" || !this.state.nextCursor)
      return Promise.resolve();
    return this.fetchPage(this.state.nextCursor, this.generation);
  };

  retry = (): Promise<void> => {
    if (this.state.phase !== "idle") return Promise.resolve();
    return this.retryCursor === null
      ? this.reload()
      : this.fetchPage(this.retryCursor, this.generation);
  };

  private async fetchPage(cursor: string, generation: number) {
    if (generation !== this.generation) return;
    const controller = new AbortController();
    this.request = controller;
    this.publish({
      ...this.state,
      phase: cursor ? "loading-more" : "loading",
      error: "",
    });
    try {
      const response = await this.loader(
        this.state.query,
        cursor,
        controller.signal,
      );
      if (generation !== this.generation || controller.signal.aborted) return;
      const page = validateToolPage(response, cursor);
      const items = new Map<string, T>(
        (cursor ? this.state.items : []).map((item) => [item.id, item]),
      );
      for (const item of page.items) items.set(item.id, item);
      this.retryCursor = null;
      this.publish({
        ...this.state,
        items: [...items.values()],
        total: page.total,
        nextCursor: page.next_cursor ?? "",
        phase: "idle",
        loaded: true,
        error: "",
      });
    } catch (error) {
      if (generation !== this.generation || controller.signal.aborted) return;
      if (
        error instanceof APIError &&
        (error.status === 401 || error.status === 403)
      ) {
        this.retryCursor = null;
        this.publish({
          ...this.state,
          items: [],
          total: null,
          nextCursor: "",
          loaded: false,
          phase: "idle",
          error: messageOf(error),
        });
        return;
      }
      this.retryCursor = cursor;
      this.publish({ ...this.state, phase: "idle", error: messageOf(error) });
    }
  }
}
