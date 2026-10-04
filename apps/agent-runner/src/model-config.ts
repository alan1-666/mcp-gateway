type ModelSelection = { provider?: string; modelId?: string };

/** Empty Compose environment variables mean no override, so persisted choices still apply. */
export function resolveModelSelection(
  environment: { PI_PROVIDER?: string; PI_MODEL?: string },
  savedModel: ModelSelection | null | undefined,
  defaults: ModelSelection,
): ModelSelection {
  return {
    provider: (environment.PI_PROVIDER?.trim() || undefined) ?? savedModel?.provider ?? defaults.provider,
    modelId: (environment.PI_MODEL?.trim() || undefined) ?? savedModel?.modelId ?? defaults.modelId,
  };
}
