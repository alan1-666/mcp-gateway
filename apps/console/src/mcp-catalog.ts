export type CatalogStatus =
  | "unimported"
  | "unchanged"
  | "schema_changed"
  | "description_changed"
  | "missing";
export interface CatalogEntry {
  name: string;
  gateway_name: string;
  status: CatalogStatus;
  schema_hash?: string;
  imported_tool_id?: string;
  imported_version?: number;
  imported_schema_hash?: string;
  imported_status?: string;
  imported_enabled: boolean;
}
export interface CatalogReview {
  source?: "manual" | "scheduled";
  id: string;
  server_id: string;
  started_at: string;
  checked_at: string;
  counts: Record<CatalogStatus, number>;
  items: CatalogEntry[];
}
export const catalogLabels: Record<CatalogStatus, string> = {
  unimported: "Not imported",
  unchanged: "In sync",
  schema_changed: "Schema changed",
  description_changed: "Description changed",
  missing: "Missing upstream",
};

export function catalogCandidate(entry: CatalogEntry, reason: string) {
  if (
    !["schema_changed", "description_changed"].includes(entry.status) ||
    !entry.imported_tool_id ||
    !Number.isSafeInteger(entry.imported_version) ||
    entry.imported_version! < 1 ||
    !/^[a-f0-9]{64}$/.test(entry.schema_hash ?? "")
  )
    throw new Error(
      "Discover tools again before preparing a changed contract.",
    );
  const trimmed = reason.trim();
  if (
    !trimmed ||
    new TextEncoder().encode(trimmed).length > 1000 ||
    trimmed.includes("\0")
  )
    throw new Error("Provide a change reason of 1–1,000 UTF-8 bytes.");
  return {
    expected_version: entry.imported_version,
    expected_schema_hash: entry.schema_hash,
    reason: trimmed,
  };
}
