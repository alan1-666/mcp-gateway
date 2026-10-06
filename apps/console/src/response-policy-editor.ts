import { withArtifactPolicy } from "./artifact-policy";
import { APIError, messageOf } from "./api";
import type { APIClient } from "./api";
import { parseObject } from "./json";
import { parseResponsePolicy } from "./mcp-servers";
import type { ResponsePolicy, Tool } from "./types";

export const examplePolicySample = JSON.stringify(
  {
    isError: false,
    content: [],
    structuredContent: {
      results: [
        { title: "Example", url: "https://example.com", details: "omitted" },
      ],
    },
  },
  null,
  2,
);

export interface PolicyPreview {
  tool_version: number;
  original_bytes: number;
  projected_bytes: number;
  result: unknown;
}

export interface PolicyEditorState {
  tool: Tool | null;
  include: string;
  maxBytes: string;
  artifactEnabled: string;
  artifactMaxBytes: string;
  artifactTTL: string;
  sample: string;
  phase: "idle" | "previewing" | "saving" | "reloading";
  preview: PolicyPreview | null;
  error: string;
  notice: string;
  needsReload: boolean;
  denied: boolean;
  dirty: boolean;
}

export function canEditResponsePolicy(canManage: boolean, tool: Tool | null) {
  return canManage && !!tool?.mcp;
}

export function previewPolicyBody(
  version: number,
  policy: ResponsePolicy,
  sample: string,
) {
  // parseObject rejects unsafe integers before JSON.stringify can send rounded IDs.
  const parsed = parseObject(sample, "Sample response");
  assertNumberRoundTrips(sample);
  const body = {
    expected_version: version,
    response_policy: policy,
    sample: parsed,
  };
  if (new TextEncoder().encode(JSON.stringify(body)).length > 256 * 1024)
    throw new Error(
      "The preview request exceeds 256 KiB. Use a smaller representative sample.",
    );
  return body;
}

// JSON.parse may round a decimal into a safe integer, or underflow it to zero.
// Compare decimal values before and after the browser's serialization, while
// accepting ordinary decimals such as 0.1 and equivalent exponent notation.
function assertNumberRoundTrips(json: string) {
  const number = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;
  let inString = false;
  for (let index = 0; index < json.length; index += 1) {
    const character = json[index];
    if (inString) {
      if (character === "\\") index += 1;
      else if (character === '"') inString = false;
      continue;
    }
    if (character === '"') {
      inString = true;
      continue;
    }
    if (character !== "-" && (character < "0" || character > "9")) continue;
    number.lastIndex = index;
    const token = number.exec(json)?.[0];
    if (!token) continue; // parseObject has already validated the JSON grammar.
    if (decimalValue(token) !== decimalValue(JSON.stringify(Number(token))))
      throw new Error(
        "Sample response contains a number that would be rounded or underflow during submission. Use a representative value the browser can preserve, or a string when the response contract allows it.",
      );
    index += token.length - 1;
  }
}

function decimalValue(token: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(token)!;
  const fraction = match[3] ?? "";
  const digits = (match[2] + fraction).replace(/^0+/, "");
  if (!digits) return "0";
  const significant = digits.replace(/0+$/, "");
  const exponent =
    BigInt(match[4] ?? "0") -
    BigInt(fraction.length) +
    BigInt(digits.length - significant.length);
  return `${match[1]}${significant}e${exponent}`;
}

const draftFor = (tool: Tool) => ({
  include: tool.response_policy?.include?.join("\n") ?? "",
  maxBytes: String(tool.response_policy?.max_bytes ?? 65536),
  artifactEnabled: String(!!tool.response_policy?.artifact),
  artifactMaxBytes: String(tool.response_policy?.artifact?.max_bytes ?? 524288),
  artifactTTL: String(tool.response_policy?.artifact?.ttl_seconds ?? 3600),
});

// One controller belongs to one selected tool. It outlives detail refreshes,
// preserving the draft while generation checks fence edited/closed previews.
export class ResponsePolicyEditorController {
  private state: PolicyEditorState = {
    tool: null,
    include: "",
    maxBytes: "65536",
    artifactEnabled: "false",
    artifactMaxBytes: "524288",
    artifactTTL: "3600",
    sample: examplePolicySample,
    phase: "idle",
    preview: null,
    error: "",
    notice: "",
    needsReload: false,
    denied: false,
    dirty: false,
  };
  private generation = 0;
  private request: AbortController | null = null;
  private listeners = new Set<() => void>();

  constructor(
    private readonly api: Pick<APIClient, "request">,
    private readonly toolID: string,
    private readonly canManage: boolean,
  ) {}

  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(next: PolicyEditorState) {
    this.state = next;
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.generation += 1;
    this.request?.abort();
    this.request = null;
  }

  observe(tool: Tool) {
    if (tool.id !== this.toolID || !canEditResponsePolicy(this.canManage, tool))
      return;
    if (!this.state.tool) {
      this.publish({ ...this.state, tool, ...draftFor(tool) });
    } else if (tool.version > this.state.tool.version) {
      this.cancel();
      if (this.state.dirty) {
        this.publish({
          ...this.state,
          phase: "idle",
          preview: null,
          needsReload: true,
          error: `This tool is now at version ${tool.version}. Reload the latest contract and review your retained draft before saving.`,
        });
      } else {
        this.publish({
          ...this.state,
          tool,
          ...draftFor(tool),
          phase: "idle",
          preview: null,
          needsReload: false,
        });
      }
    }
  }

  edit(
    field:
      | "include"
      | "maxBytes"
      | "sample"
      | "artifactEnabled"
      | "artifactMaxBytes"
      | "artifactTTL",
    value: string,
  ) {
    if (this.state.phase === "saving" || this.state.phase === "reloading")
      return;
    this.cancel();
    this.publish({
      ...this.state,
      [field]: value,
      phase: "idle",
      preview: null,
      dirty: this.state.dirty || field !== "sample",
      notice: "",
      error: this.state.needsReload ? this.state.error : "",
    });
  }

  private assertEditable() {
    if (
      !canEditResponsePolicy(this.canManage, this.state.tool) ||
      this.state.denied
    )
      throw new Error(
        "Only administrators can edit or preview an MCP tool’s response policy.",
      );
    if (this.state.needsReload)
      throw new Error(
        "Reload the latest contract and review your retained draft before continuing. No changes have been retried.",
      );
    return this.state.tool!;
  }

  private begin(phase: PolicyEditorState["phase"]) {
    this.cancel();
    this.request = new AbortController();
    this.publish({
      ...this.state,
      phase,
      error: "",
      notice: "",
      preview: null,
    });
    return { generation: this.generation, signal: this.request.signal };
  }

  private fail(error: unknown, saving = false) {
    const denied =
      this.state.denied ||
      (error instanceof APIError && [401, 403].includes(error.status));
    const conflict = error instanceof APIError && error.status === 409;
    const uncertain =
      saving && (!(error instanceof APIError) || error.status >= 500);
    this.publish({
      ...this.state,
      phase: "idle",
      preview: null,
      denied,
      needsReload: this.state.needsReload || conflict || uncertain,
      error: conflict
        ? "This tool changed since you opened it. Your inputs are retained. Reload the latest contract and review them before saving; this request will not be retried automatically."
        : messageOf(error) +
          (uncertain
            ? " The update may have completed. Reload the latest contract before saving again."
            : ""),
    });
  }

  preview = async (): Promise<void> => {
    if (this.state.phase !== "idle") return;
    let generation = this.generation;
    try {
      const tool = this.assertEditable();
      const policy = withArtifactPolicy(
        parseResponsePolicy(this.state.include, this.state.maxBytes),
        this.state.artifactEnabled,
        this.state.artifactMaxBytes,
        this.state.artifactTTL,
      );
      const body = previewPolicyBody(tool.version, policy, this.state.sample);
      const request = this.begin("previewing");
      generation = request.generation;
      const preview = await this.api.request<PolicyPreview>(
        `/tools/${encodeURIComponent(this.toolID)}/response-policy/preview`,
        {
          method: "POST",
          body,
          signal: request.signal,
        },
      );
      if (generation !== this.generation || request.signal.aborted) return;
      if (
        preview.tool_version !== tool.version ||
        !Number.isSafeInteger(preview.original_bytes) ||
        preview.original_bytes < 0 ||
        !Number.isSafeInteger(preview.projected_bytes) ||
        preview.projected_bytes < 0 ||
        !("result" in preview)
      )
        throw new Error(
          "The gateway returned an invalid preview. Preview the sample again.",
        );
      this.publish({ ...this.state, phase: "idle", preview, error: "" });
    } catch (error) {
      if (generation === this.generation) this.fail(error);
    }
  };

  save = async (): Promise<Tool | null> => {
    if (this.state.phase !== "idle") return null;
    let generation = this.generation;
    let dispatched = false;
    try {
      const tool = this.assertEditable();
      const policy = withArtifactPolicy(
        parseResponsePolicy(this.state.include, this.state.maxBytes),
        this.state.artifactEnabled,
        this.state.artifactMaxBytes,
        this.state.artifactTTL,
      );
      const request = this.begin("saving");
      generation = request.generation;
      dispatched = true;
      const updated = await this.api.request<Tool>(
        `/tools/${encodeURIComponent(this.toolID)}/response-policy`,
        {
          method: "POST",
          body: { expected_version: tool.version, response_policy: policy },
          signal: request.signal,
        },
      );
      if (generation !== this.generation || request.signal.aborted) return null;
      if (
        updated.id !== this.toolID ||
        !updated.mcp ||
        (updated.version !== tool.version &&
          updated.version !== tool.version + 1)
      )
        throw new Error("The update returned an unexpected tool version.");
      this.publish({
        ...this.state,
        tool: updated,
        ...draftFor(updated),
        phase: "idle",
        dirty: false,
        needsReload: false,
        notice:
          updated.version === tool.version
            ? `Policy already matches version ${updated.version}; no version was added.`
            : `Saved as version ${updated.version}. New operations use this policy; already prepared operations keep their existing snapshot.`,
      });
      return updated;
    } catch (error) {
      if (generation === this.generation) this.fail(error, dispatched);
      return null;
    }
  };

  reload = async (): Promise<Tool | null> => {
    if (this.state.phase !== "idle" || !this.canManage || this.state.denied)
      return null;
    const request = this.begin("reloading");
    try {
      const tool = await this.api.request<Tool>(
        `/tools/${encodeURIComponent(this.toolID)}`,
        { signal: request.signal },
      );
      if (request.generation !== this.generation || request.signal.aborted)
        return null;
      if (tool.id !== this.toolID || !tool.mcp)
        throw new Error(
          "The tool is no longer available as an MCP integration.",
        );
      // Explicit reload updates the expected version but never submits the draft.
      this.publish({
        ...this.state,
        tool,
        phase: "idle",
        needsReload: false,
        error: "",
        dirty: true,
        notice: `Loaded version ${tool.version}. Your draft and sample are retained; review them against the saved policy before saving.`,
      });
      return tool;
    } catch (error) {
      if (request.generation === this.generation) this.fail(error);
      return null;
    }
  };
}
