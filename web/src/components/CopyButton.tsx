"use client";

import { useState } from "react";

// CopyButton copies `value` to the clipboard and flashes "Đã copy" briefly.
// Falls back to a hidden textarea + execCommand when the Clipboard API is
// unavailable (non-secure contexts).
export default function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        const ta = document.createElement("textarea");
        ta.value = value;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — leave the value selectable instead */
    }
  }

  return (
    <button
      type="button"
      onClick={copy}
      className="shrink-0 rounded-md border border-[var(--border)] px-2.5 py-1 text-[12px] text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-[var(--text)]"
    >
      {copied ? "Đã copy" : label}
    </button>
  );
}
