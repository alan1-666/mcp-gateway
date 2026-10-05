import { useId, useState } from "react";
import type { ReactNode } from "react";

// Load on first use, then retain panels so switching cannot discard actions or drafts.
export function SectionTabs({
  label,
  tabs,
  value,
  onChange,
}: {
  label: string;
  tabs: { id: string; label: string; content: ReactNode }[];
  value: string;
  onChange: (id: string) => void;
}) {
  const id = useId();
  const selected = tabs.some((tab) => tab.id === value) ? value : tabs[0]?.id;
  const [visited, setVisited] = useState(() => new Set([selected]));
  function activate(next: string) {
    setVisited((current) => new Set([...current, selected, next]));
    onChange(next);
  }
  return (
    <div className="section-tabs">
      <div role="tablist" aria-label={label} className="section-tab-list">
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            id={`${id}-${tab.id}-tab`}
            aria-controls={`${id}-${tab.id}-panel`}
            aria-selected={selected === tab.id}
            tabIndex={selected === tab.id ? 0 : -1}
            onClick={() => activate(tab.id)}
            onKeyDown={(event) => {
              const next =
                event.key === "ArrowRight"
                  ? (index + 1) % tabs.length
                  : event.key === "ArrowLeft"
                    ? (index + tabs.length - 1) % tabs.length
                    : event.key === "Home"
                      ? 0
                      : event.key === "End"
                        ? tabs.length - 1
                        : -1;
              if (next < 0) return;
              event.preventDefault();
              activate(tabs[next].id);
              const buttons =
                event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>(
                  '[role="tab"]',
                );
              buttons?.[next]?.focus();
            }}
          >
            {tab.label}
          </button>
        ))}
      </div>
      {tabs.map((tab) => (
        <div
          key={tab.id}
          role="tabpanel"
          tabIndex={0}
          id={`${id}-${tab.id}-panel`}
          aria-labelledby={`${id}-${tab.id}-tab`}
          hidden={selected !== tab.id}
        >
          {selected === tab.id || visited.has(tab.id) ? tab.content : null}
        </div>
      ))}
    </div>
  );
}
