import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { APIClient, messageOf } from "./api";
import { ToolSearchController, toolSearchPath } from "./tool-search";
import { fetchToolDetails } from "./tool-details";
import type { ToolSearchScope } from "./tool-search";
import type { Tool, ToolPage } from "./types";

export function useToolSearch<T extends { id: string }>(
  api: APIClient,
  scope: ToolSearchScope,
  refreshVersion: string,
  serverID = "",
) {
  const controller = useMemo(
    () =>
      new ToolSearchController<T>((query, cursor, signal) =>
        api.request<ToolPage<T>>(toolSearchPath(scope, query, cursor, undefined, serverID), {
          signal,
        }),
      ),
    [api, scope, serverID],
  );
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  useEffect(() => {
    void controller.reload();
    return () => controller.cancel();
  }, [controller, refreshVersion]);
  return { state, controller };
}

export function useToolDetails(
  api: APIClient,
  id: string | null,
  publishedOnly: boolean,
  refreshVersion: string,
) {
  const [revision, setRevision] = useState(0);
  const [record, setRecord] = useState<{
    id: string | null;
    tool: Tool | null;
    error: string;
    loading: boolean;
  }>({ id: null, tool: null, error: "", loading: false });
  useEffect(() => {
    if (!id) {
      setRecord({ id: null, tool: null, error: "", loading: false });
      return;
    }
    const controller = new AbortController();
    setRecord({ id, tool: null, error: "", loading: true });
    void (async () => {
      try {
        const tool = await fetchToolDetails(
          api,
          id,
          publishedOnly,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setRecord({ id, tool, error: "", loading: false });
      } catch (error) {
        if (!controller.signal.aborted)
          setRecord({
            id,
            tool: null,
            error: messageOf(error),
            loading: false,
          });
      }
    })();
    return () => controller.abort();
  }, [api, id, publishedOnly, refreshVersion, revision]);
  const current =
    record.id === id ? record : { id, tool: null, error: "", loading: !!id };
  return { ...current, reload: () => setRevision((value) => value + 1) };
}
