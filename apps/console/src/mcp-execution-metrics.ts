export interface MCPExecutionMetrics {
  server_id: string;
  observed_at: string;
  window_start: string;
  operations_scanned: number;
  sample_limit: number;
  truncated: boolean;
  calls: number;
  attempted: number;
  states: Record<string, number>;
  errors: Record<string, number>;
  p50_ms: number;
  p95_ms: number;
  mean_phases_ms: Record<string, number>;
  health: string;
  last_observed_at?: string;
  last_code?: string;
}
const healthLabels: Record<string, string> = {
  healthy: "Last call succeeded",
  degraded: "Last call encountered a transport failure",
  attention: "Last call needs review",
  stale: "Observation is stale",
  disabled: "Service is disabled",
  unobserved: "No calls in this sample",
};
export function executionHealthLabel(health: string): string {
  return healthLabels[health] ?? "Observation unavailable";
}
export const executionPhaseLabels: Record<string, string> = {
  configuration: "Configuration validation",
  session: "Session acquisition",
  catalog: "Catalog verification",
  call: "Tool round trip",
  result: "Result validation",
  projection: "Response projection",
};
