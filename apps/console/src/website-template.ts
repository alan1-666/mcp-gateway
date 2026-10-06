import { chinese, english } from "./website-copy";
import type { WebsiteLanguage } from "./website-routing";

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!,
  );
}

/** Static HTML for each language: no model, API call or client translation needed. */
export function renderWebsite(template: string, language: WebsiteLanguage): string {
  const copy = language === "zh" ? chinese : english;
  const messages: Record<string, string> = {
    ...copy,
    lang: language === "zh" ? "zh-CN" : "en",
    ogLocale: language === "zh" ? "zh_CN" : "en_US",
    homePath: `/${language}/`,
    enCurrent: language === "en" ? "page" : "false",
    zhCurrent: language === "zh" ? "page" : "false",
  };
  return template.replace(/\{\{([a-zA-Z]+)\}\}/g, (_, key: string) => {
    if (!Object.hasOwn(messages, key)) throw new Error(`Missing website message: ${key}`);
    return escapeHTML(messages[key]);
  });
}
