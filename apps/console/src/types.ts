export interface Identity {
  id: string;
  workspace_id: string;
  role: string;
}

export interface HTTPConfiguration {
  url: string;
  method: string;
  credential_ref?: string;
  timeout_ms: number;
}

export interface Tool {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  risk: "read" | "write";
  input_schema: Record<string, unknown>;
  output_schema?: Record<string, unknown>;
  http: HTTPConfiguration;
  status: "draft" | "published";
  enabled: boolean;
  version: number;
  created_at: string;
}

export type ToolSummary = Pick<
  Tool,
  "id" | "name" | "description" | "risk" | "version"
>;

export interface ToolPage<T = Tool> {
  items: T[];
  next_cursor?: string;
  total: number;
}

export type OperationState =
  | "WAITING_APPROVAL"
  | "READY"
  | "DISPATCHING"
  | "SUCCEEDED"
  | "FAILED"
  | "UNKNOWN"
  | "REJECTED";

export interface Operation {
  id: string;
  tool_id: string;
  tool_name: string;
  tool_version: number;
  actor_id: string;
  arguments: Record<string, unknown>;
  arguments_hash: string;
  idempotency_key: string;
  state: OperationState;
  result?: unknown;
  error?: string;
  created_at: string;
  updated_at: string;
  approved_by?: string;
  approval_expires_at?: string;
}

export interface OperationEvent {
  id: number;
  operation_id: string;
  type: string;
  actor_id: string;
  created_at: string;
  data: Record<string, unknown>;
}
