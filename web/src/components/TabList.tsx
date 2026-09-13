"use client";

import { useRef } from "react";

export interface TabItem {
  id: string;
  label: string;
}

// TabList is an accessible tab strip: roving tabindex (only the selected tab is
// in the tab order) + Arrow/Home/End keyboard navigation, per the WAI-ARIA tabs
// pattern. Panels stay in the parent and are linked by id (`panel-<id>`).
export default function TabList({
  tabs,
  active,
  onChange,
  label,
}: {
  tabs: TabItem[];
  active: string;
  onChange: (id: string) => void;
  label: string;
}) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});

  function onKeyDown(e: React.KeyboardEvent<HTMLButtonElement>) {
    const i = tabs.findIndex((t) => t.id === active);
    let next: number;
    if (e.key === "ArrowRight") next = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = tabs.length - 1;
    else return;
    e.preventDefault();
    const id = tabs[next].id;
    onChange(id);
    refs.current[id]?.focus();
  }

  return (
    <div role="tablist" aria-label={label} className="mb-6 flex gap-1 border-b border-[var(--border)]">
      {tabs.map((t) => {
        const selected = t.id === active;
        return (
          <button
            key={t.id}
            ref={(el) => {
              refs.current[t.id] = el;
            }}
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={selected}
            aria-controls={`panel-${t.id}`}
            tabIndex={selected ? 0 : -1}
            onClick={() => onChange(t.id)}
            onKeyDown={onKeyDown}
            className={`rounded-t-md px-4 py-2 text-[13px] ${
              selected
                ? "border-b-2 border-[var(--accent)] text-[var(--text)]"
                : "text-[var(--text2)] hover:text-[var(--text)]"
            }`}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}
