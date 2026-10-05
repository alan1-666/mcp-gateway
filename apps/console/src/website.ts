import "@fontsource/dm-sans/latin-400.css";
import "@fontsource/dm-sans/latin-500.css";
import "@fontsource/dm-sans/latin-600.css";
import "@fontsource/dm-sans/latin-700.css";
import "@fontsource/ibm-plex-mono/latin-400.css";
import "./website.css";
import { legacyInvitationDestination } from "./website-routing";

function forwardLegacyInvitation() {
  const invitation = legacyInvitationDestination(window.location.hash);
  if (invitation) window.location.replace(invitation);
}
forwardLegacyInvitation();
window.addEventListener("hashchange", forwardLegacyInvitation);

// Entirely local, synthetic data: this example never calls the gateway.
const sample = {
  results: [
    {
      title: "Connect your first server",
      url: "https://docs.example/connect",
      content:
        "Register an upstream, discover its tools, review the contract, and publish.",
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
const code = document.querySelector<HTMLElement>("#response-code")!;
const note = document.querySelector<HTMLElement>("#response-note")!;
const buttons = document.querySelectorAll<HTMLButtonElement>("[data-view]");
buttons.forEach((button) => {
  button.addEventListener("click", () => {
    const selected = button.dataset.view === "projected";
    code.textContent = JSON.stringify(selected ? projected : sample, null, 2);
    note.textContent = selected
      ? "2 selected fields · Pagination retained"
      : "6 upstream fields · Before field selection";
    buttons.forEach((item) =>
      item.setAttribute("aria-pressed", String(item === button)),
    );
  });
});
