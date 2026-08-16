"use client";

import { useState } from "react";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  const diff = Date.now() - then;
  const m = Math.floor(diff / 60000);
  if (m < 1) return "vừa xong";
  if (m < 60) return `${m} phút trước`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} giờ trước`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d} ngày trước`;
  return new Date(iso).toLocaleDateString("vi-VN");
}

interface Props {
  sessions: SessionSummary[];
  activeId: string;
  onSelect: (id: string) => void;
  onNew: () => void;
  onChanged: () => void;
}

export default function SessionsSidebar({ sessions, activeId, onSelect, onNew, onChanged }: Props) {
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  async function rename(id: string) {
    const title = draft.trim();
    if (!title) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "PATCH", body: { title } });
    setRenamingId(null);
    onChanged();
  }

  async function remove(id: string) {
    if (!window.confirm("Xoá hội thoại này?")) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "DELETE" });
    if (id === activeId) onNew();
    onChanged();
  }

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)]">
      <div className="border-b border-[var(--border)] p-3">
        <button
          onClick={onNew}
          className="w-full rounded-md bg-[var(--accent)] px-3 py-2 text-[13px] font-medium text-white hover:opacity-90"
        >
          ＋ Chat mới
        </button>
      </div>
      <div className="flex-1 overflow-y-auto py-1">
        {sessions.length === 0 && (
          <div className="px-4 py-6 text-center text-[12px] text-[var(--text2)]">Chưa có hội thoại nào.</div>
        )}
        {sessions.map((s) => (
          <div
            key={s.id}
            className={`group flex items-center gap-2 px-3 py-2 text-[13px] ${
              s.id === activeId ? "bg-[var(--surface2)] text-white" : "text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-white"
            }`}
          >
            {renamingId === s.id ? (
              <form
                className="flex-1"
                onSubmit={(e) => {
                  e.preventDefault();
                  rename(s.id);
                }}
              >
                <input
                  autoFocus
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  onBlur={() => rename(s.id)}
                  className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-1 py-0.5 text-[13px] outline-none"
                />
              </form>
            ) : (
              <button className="min-w-0 flex-1 text-left" onClick={() => onSelect(s.id)}>
                <div className="truncate">{s.title || "Hội thoại mới"}</div>
                <div className="text-[10px] text-[var(--text2)]">{relativeTime(s.updated_at)}</div>
              </button>
            )}
            <div className="hidden gap-1 group-hover:flex">
              <button
                className="text-[11px] text-[var(--text2)] hover:text-white"
                onClick={() => {
                  setRenamingId(s.id);
                  setDraft(s.title);
                }}
              >
                ✎
              </button>
              <button className="text-[11px] text-[var(--err)]" onClick={() => remove(s.id)}>
                ✕
              </button>
            </div>
          </div>
        ))}
      </div>
    </aside>
  );
}
