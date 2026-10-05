/** Old invitations were issued at the site root. Never forward arbitrary URLs. */
export function legacyInvitationDestination(hash: string): string | null {
  if (!hash.startsWith("#invite=")) return null;
  const token = new URLSearchParams(hash.slice(1)).get("invite");
  return token ? `/console/#invite=${encodeURIComponent(token)}` : null;
}
