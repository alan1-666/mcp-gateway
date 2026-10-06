import { createContext, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  initialLanguage,
  languageLocale,
  persistLanguage,
  translate,
} from "./console-language";
import type { Language, TranslationValues } from "./console-language";
import { languagePreferenceKey } from "./website-routing";

type I18n = {
  language: Language;
  locale: string;
  t: (source: string, values?: TranslationValues) => string;
  setLanguage: (language: Language) => void;
};
const I18nContext = createContext<I18n | null>(null);
function availableStorage(): Storage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

export function I18nProvider({
  children,
  initial,
}: {
  children: ReactNode;
  initial?: Language;
}) {
  const [language, setLanguage] = useState<Language>(
    () => initial ?? initialLanguage(availableStorage(), navigator.languages),
  );
  useEffect(() => {
    document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
    document.title =
      language === "zh" ? "Rillgate · 控制台" : "Rillgate · Console";
    persistLanguage(availableStorage(), language);
  }, [language]);
  useEffect(() => {
    const sync = (event: StorageEvent) => {
      if (
        event.key !== languagePreferenceKey ||
        (event.newValue !== "en" &&
          event.newValue !== "zh" &&
          event.newValue !== "cn")
      )
        return;
      setLanguage(event.newValue === "cn" ? "zh" : event.newValue);
    };
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, []);
  const value = useMemo<I18n>(
    () => ({
      language,
      locale: languageLocale(language),
      setLanguage,
      t: (source, values) => translate(language, source, values),
    }),
    [language],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18n {
  const value = useContext(I18nContext);
  if (!value) throw new Error("Console translations require I18nProvider");
  return value;
}

export function LanguageSwitcher() {
  const { language, setLanguage, t } = useI18n();
  return (
    <div className="console-language" role="group" aria-label={t("Language")}>
      <button
        type="button"
        lang="en"
        aria-pressed={language === "en"}
        onClick={() => setLanguage("en")}
      >
        EN
      </button>
      <span aria-hidden="true">/</span>
      <button
        type="button"
        lang="zh-CN"
        aria-pressed={language === "zh"}
        onClick={() => setLanguage("zh")}
      >
        中文
      </button>
    </div>
  );
}
