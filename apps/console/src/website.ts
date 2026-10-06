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

// Local illustration only: changing the scenario never sends a business request.
const controls = document.querySelector<HTMLElement>("[data-scenario-controls]");
const scenarioButtons = document.querySelectorAll<HTMLButtonElement>("[data-scenario]");
const outcomes = document.querySelectorAll<HTMLElement>("[data-outcome]");
scenarioButtons.forEach((button) => {
  button.addEventListener("click", () => {
    outcomes.forEach((outcome) => {
      outcome.hidden = outcome.dataset.outcome !== button.dataset.scenario;
    });
    scenarioButtons.forEach((item) => {
      item.setAttribute("aria-pressed", String(item === button));
    });
  });
});
if (controls) controls.hidden = false;
