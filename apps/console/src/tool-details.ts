import type { APIClient } from "./api";
import type { Tool } from "./types";

// Details are fetched independently of the loaded search page. A linked tool
// can be older than the current page, and publication can change after search.
export async function fetchToolDetails(
  api: Pick<APIClient, "request">,
  id: string,
  publishedOnly: boolean,
  signal?: AbortSignal,
): Promise<Tool> {
  const tool = await api.request<Tool>(`/tools/${encodeURIComponent(id)}`, {
    signal,
  });
  if (tool.id !== id)
    throw new Error(
      "The gateway returned a different tool. Select the tool again.",
    );
  if (publishedOnly && (tool.status !== "published" || !tool.enabled))
    throw new Error(
      "This tool is no longer published and enabled. Refresh the catalog and select another tool.",
    );
  return tool;
}
