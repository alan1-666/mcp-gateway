/** Old reports did not check independent sessions. Unknown values must not
 * become a positive compatibility signal. Protocol codes remain unchanged. */
export function sessionContractLabel(status?: string): string {
  switch (status) {
    case "stable":
      return "Stable in two observed sessions";
    case "changed":
      return "Contract changed between sessions";
    case "unverified":
      return "Fresh session could not be verified";
    default:
      return "Not checked";
  }
}
