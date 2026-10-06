import "@fontsource/dm-sans/latin-400.css";
import "@fontsource/dm-sans/latin-500.css";
import "@fontsource/dm-sans/latin-600.css";
import "@fontsource/dm-sans/latin-700.css";
import "@fontsource/ibm-plex-mono/latin-400.css";
import "./website.css";
import {
  languageFromPath,
  readLanguagePreference,
  saveLanguagePreference,
  websiteDestination,
} from "./website-routing";

// Access to localStorage itself can throw in restricted browsing contexts.
let storage: Storage | undefined;
try {
  storage = window.localStorage;
} catch {
  // Explicit language links remain usable.
}
function routeWebsite() {
  const preference = storage ? readLanguagePreference(storage) : null;
  const destination = websiteDestination(window.location, preference);
  if (destination) {
    window.location.replace(destination);
    return;
  }
  const language = languageFromPath(window.location.pathname);
  if (language && storage) saveLanguagePreference(storage, language);
}
routeWebsite();
window.addEventListener("hashchange", routeWebsite);
document.querySelectorAll<HTMLAnchorElement>("[data-language]").forEach((link) => {
  link.addEventListener("click", () => {
    const language = link.dataset.language;
    if (language !== "en" && language !== "cn") return;
    if (storage) saveLanguagePreference(storage, language);
    link.href = `/${language}/${window.location.search}${window.location.hash}`;
  });
});

// Entirely local, synthetic data: this example never calls the gateway.
const code = document.querySelector<HTMLElement>("#response-code")!;
const sample = {
  results: [
    {
      title: code.dataset.sampleTitle!,
      url: "https://docs.example/connect",
      content: code.dataset.sampleContent!,
      source: "documentation",
      updatedAt: "2026-10-01",
      score: 0.94,
    },
  ],
  nextCursor: "page_2",
};
const projected = {
  results: sample.results.map(({ title, url }) => ({ title, url })),
  nextCursor: sample.nextCursor,
};
const note = document.querySelector<HTMLElement>("#response-note")!;
const buttons = document.querySelectorAll<HTMLButtonElement>("[data-view]");
buttons.forEach((button) => {
  button.addEventListener("click", () => {
    const selected = button.dataset.view === "projected";
    code.textContent = JSON.stringify(selected ? projected : sample, null, 2);
    note.textContent = selected
      ? code.dataset.projectedNote!
      : code.dataset.upstreamNote!;
    buttons.forEach((item) =>
      item.setAttribute("aria-pressed", String(item === button)),
    );
  });
});
