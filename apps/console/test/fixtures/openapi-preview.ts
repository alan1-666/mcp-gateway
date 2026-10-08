// Cross-language fixture: use the production parser, never a hand-maintained ToolInput.
import { readFileSync } from "node:fs";
import { previewOpenAPI } from "../../src/openapi-import";
const operations = previewOpenAPI(readFileSync(0, "utf8"));
if (operations.some((operation) => !operation.candidate)) {
  throw new Error(JSON.stringify(operations));
}
process.stdout.write(
  JSON.stringify(
    operations.map(({ candidate }) => ({
      name: candidate!.name,
      description: candidate!.description,
      risk: "write", // Same conservative default as the console review form.
      input_schema: candidate!.inputSchema,
      http: {
        url: candidate!.url,
        method: candidate!.method,
        timeout_ms: 2000,
      },
    })),
  ),
);
