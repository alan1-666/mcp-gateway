import {
  readLanguagePreference,
  saveLanguagePreference,
} from "./website-routing";
import type { WebsiteLanguage } from "./website-routing";
import { appZh } from "./locale-app";
import { accessZh } from "./locale-access";
import { mcpZh } from "./locale-mcp";
import { toolsZh } from "./locale-tools";
import { dynamicMessageTemplates, errorsZh } from "./locale-errors";

export type Language = WebsiteLanguage;
export type TranslationValues = Readonly<Record<string, string | number>>;
export const chineseCopy: Readonly<Record<string, string>> = Object.freeze({
  ...accessZh,
  ...mcpZh,
  ...toolsZh,
  // Shared navigation/field terms take precedence across feature dictionaries.
  ...appZh,
  ...errorsZh,
});

const dynamicMessages = dynamicMessageTemplates.map((template) => {
  const names: string[] = [];
  const pattern = template
    .split(/(\{[A-Za-z]+\})/)
    .map((part) => {
      if (part.startsWith("{")) {
        const name = part.slice(1, -1);
        names.push(name);
        return name === "label" ? "([^\\r\\n]{1,120})" : "([0-9]+)";
      }
      return part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    })
    .join("");
  return { template, names, pattern: new RegExp(`^${pattern}$`) };
});

/** Only known UI copy is translated; upstream messages and user data pass through. */
export function translate(
  language: Language,
  source: string,
  values?: TranslationValues,
): string {
  if (language === "zh" && !Object.hasOwn(chineseCopy, source)) {
    for (const entry of dynamicMessages) {
      const match = source.match(entry.pattern);
      if (match) {
        const captured = Object.fromEntries(
          entry.names.map((name, i) => [
            name,
            name === "label" ? translate(language, match[i + 1]) : match[i + 1],
          ]),
        );
        return translate(language, entry.template, captured);
      }
    }
    for (const suffix of [
      "The update may have completed. Reload the latest contract before saving again.",
      "The server list has been refreshed; check its current state before trying again.",
      "Reload the schedule to check its saved state before trying again.",
      "Discover tools again before retrying; the previous import may have completed.",
      "Refresh status before trying again; the previous action may have completed.",
      "Refresh the list before creating another connector; registration may have completed. If its token was lost, revoke it and create a new registration.",
      "Refresh the list to check whether revocation completed.",
    ]) {
      if (source.endsWith(` ${suffix}`))
        return `${translate(language, source.slice(0, -suffix.length - 1))} ${translate(language, suffix)}`;
    }
  }
  const copy =
    language === "zh" && Object.hasOwn(chineseCopy, source)
      ? chineseCopy[source]
      : source;
  // Replace only caller-provided placeholders, in a single pass. Values are never parsed as markup.
  return copy.replace(
    /\{([A-Za-z][A-Za-z0-9_]*)\}/g,
    (placeholder, name: string) =>
      values && Object.hasOwn(values, name)
        ? String(values[name])
        : placeholder,
  );
}

export function initialLanguage(
  storage?: Pick<Storage, "getItem">,
  languages: readonly string[] = [],
): Language {
  const saved = storage ? readLanguagePreference(storage) : null;
  if (saved) return saved;
  return languages[0]?.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export function persistLanguage(
  storage: Pick<Storage, "setItem"> | undefined,
  language: Language,
): void {
  if (storage) saveLanguagePreference(storage, language);
}

export function languageLocale(language: Language): string {
  return language === "zh" ? "zh-CN" : "en-US";
}
