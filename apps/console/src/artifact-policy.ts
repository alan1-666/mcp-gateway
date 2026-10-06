import type { ResponsePolicy } from "./types";

export function withArtifactPolicy(
  policy: ResponsePolicy,
  enabled: string,
  maxBytes: string,
  ttlSeconds: string,
): ResponsePolicy {
  if (enabled === "false") return policy;
  if (enabled !== "true")
    throw new Error("Choose whether large-result storage is enabled.");
  const maximum = Number(maxBytes);
  const ttl = Number(ttlSeconds);
  if (
    !Number.isSafeInteger(maximum) ||
    maximum < policy.max_bytes ||
    maximum > 1048576
  ) {
    throw new Error(
      "Large-result limit must be an integer between the inline limit and 1,048,576 bytes.",
    );
  }
  if (!Number.isSafeInteger(ttl) || ttl < 60 || ttl > 86400) {
    throw new Error(
      "Large-result retention must be an integer between 60 and 86,400 seconds.",
    );
  }
  return { ...policy, artifact: { max_bytes: maximum, ttl_seconds: ttl } };
}
