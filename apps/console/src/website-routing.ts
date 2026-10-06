/** Old invitations were issued at the site root. Never forward arbitrary URLs. */
export function legacyInvitationDestination(hash: string): string | null {
  if (!hash.startsWith("#invite=")) return null;
  const token = new URLSearchParams(hash.slice(1)).get("invite");
  return token ? `/console/#invite=${encodeURIComponent(token)}` : null;
}

export type WebsiteLanguage = "en" | "cn";
export const languagePreferenceKey = "rillgate.website.language";

export function languageFromPath(pathname: string): WebsiteLanguage | null {
  if (/^\/cn(?:\/|\/index\.html)?$/.test(pathname)) return "cn";
  if (/^\/en(?:\/|\/index\.html)?$/.test(pathname)) return "en";
  return null;
}

export function readLanguagePreference(storage: Pick<Storage, "getItem">): WebsiteLanguage | null {
  try {
    const value = storage.getItem(languagePreferenceKey);
    return value === "cn" || value === "en" ? value : null;
  } catch {
    return null;
  }
}

export function saveLanguagePreference(storage: Pick<Storage, "setItem">, language: WebsiteLanguage): void {
  try {
    storage.setItem(languagePreferenceKey, language);
  } catch {
    // Navigation still works without storage.
  }
}

export function websiteDestination(
  location: Pick<Location, "pathname" | "search" | "hash">,
  preference: WebsiteLanguage | null,
): string | null {
  // Invitation handling always wins, even when a language was remembered.
  const invitation = legacyInvitationDestination(location.hash);
  if (invitation) return invitation;
  if ((location.pathname === "/" || location.pathname === "/index.html") && preference) {
    return `/${preference}/${location.search}${location.hash}`;
  }
  return null;
}
