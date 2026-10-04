export function hasUnsafeNumbers(value: unknown): boolean {
  if (typeof value === "number") {
    return (
      !Number.isFinite(value) ||
      (Number.isInteger(value) && !Number.isSafeInteger(value))
    );
  }
  if (Array.isArray(value)) return value.some(hasUnsafeNumbers);
  if (value && typeof value === "object")
    return Object.values(value).some(hasUnsafeNumbers);
  return false;
}

export function parseObject(
  value: string,
  label: string,
): Record<string, unknown> {
  let result: unknown;
  try {
    result = JSON.parse(value);
  } catch {
    throw new Error(`${label} must be valid JSON.`);
  }
  if (!result || typeof result !== "object" || Array.isArray(result)) {
    throw new Error(`${label} must be a JSON object.`);
  }
  if (hasUnsafeNumbers(result)) {
    throw new Error(
      `${label} contains a number outside the browser's exact numeric range. Use a string for large identifiers or high-precision values, with a tool schema that accepts strings.`,
    );
  }
  return result as Record<string, unknown>;
}
