"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import BrandMark from "@/components/BrandMark";
import { useAuth } from "@/context/AuthContext";
import { useChatSessions } from "@/context/ChatSessionsContext";
import { isPlatformAdmin, ROLE_LABEL } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

const PRIMARY = [
  { href: "/chat", label: "Chat", d: "M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z" },
  { href: "/platform", label: "Platform", d: "M3 3v18h18M7 16v-4M12 16V8M17 16v-6" },
];

const ADMIN = [
  { href: "/infra", label: "Infra", d: "M12 2 2 7l10 5 10-5-10-5zM2 12l10 5 10-5M2 17l10 5 10-5" },
  { href: "/admin", label: "Admin", d: "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" },
];

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

function Item({ href, label, d, onNavigate }: { href: string; label: string; d: string; onNavigate?: () => void }) {
  const pathname = usePathname();
  const active = pathname.startsWith(href);
  return (
    <Link
      href={href}
      onClick={onNavigate}
      className={`flex items-center gap-2.5 rounded-lg px-3 py-2 text-[13px] ${
        active
          ? "bg-[var(--surface2)] text-[var(--text)]"
          : "text-[var(--text2)] hover:bg-[var(--surface)] hover:text-[var(--text)]"
      }`}
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="h-4 w-4 shrink-0"
        aria-hidden="true"
      >
        <path d={d} />
      </svg>
      {label}
    </Link>
  );
}

export default function Sidebar({ open = false, onClose }: { open?: boolean; onClose?: () => void }) {
  const { claims, logout } = useAuth();
  const { sessions, activeId, select, newChat, refresh } = useChatSessions();
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  const isAdmin = !!claims && isPlatformAdmin(claims.role);

  async function rename(id: string) {
    const title = draft.trim();
    if (!title) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "PATCH", body: { title } });
    setRenamingId(null);
    refresh();
  }

  async function remove(id: string) {
    if (!window.confirm("Xoá hội thoại này?")) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "DELETE" });
    if (id === activeId) newChat();
    refresh();
  }

  return (
    <aside
      className={`fixed inset-y-0 left-0 z-40 flex w-64 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)] transition-transform duration-200 md:static md:z-auto md:translate-x-0 ${
        open ? "translate-x-0" : "-translate-x-full"
      }`}
    >
      <div className="flex items-center gap-2.5 px-4 py-4">
        <BrandMark size={26} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[14px] font-semibold leading-tight">AI Factory</div>
          <div className="text-[11px] text-[var(--text2)]">Inference</div>
        </div>
        <button
          onClick={onClose}
          aria-label="Đóng menu"
          className="flex h-7 w-7 items-center justify-center rounded-lg text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-[var(--text)] md:hidden"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-4 w-4" aria-hidden="true">
            <path d="M18 6 6 18M6 6l12 12" />
          </svg>
        </button>
      </div>

      <div className="px-3 pb-2">
        <button
          onClick={() => {
            newChat();
            onClose?.();
          }}
          className="flex w-full items-center justify-center gap-2 rounded-lg bg-[var(--accent-strong)] px-3 py-2 text-[13px] font-medium text-white transition-opacity hover:opacity-90"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" className="h-4 w-4" aria-hidden="true">
            <path d="M12 5v14M5 12h14" />
          </svg>
          Chat mới
        </button>
      </div>

      <nav className="space-y-0.5 px-3 py-1">
        {PRIMARY.map((it) => (
          <Item key={it.href} {...it} onNavigate={onClose} />
        ))}
      </nav>

      <div className="flex min-h-0 flex-1 flex-col border-t border-[var(--border)] pt-2">
        <div className="flex-1 overflow-y-auto px-3 py-1">
          {sessions.length === 0 && (
            <div className="px-2 py-6 text-center text-[12px] text-[var(--text2)]">Chưa có hội thoại nào.</div>
          )}
          {sessions.map((s) => (
            <div
              key={s.id}
              className={`group flex items-center gap-2 rounded-lg px-3 py-2 text-[13px] ${
                s.id === activeId
                  ? "bg-[var(--surface2)] text-[var(--text)]"
                  : "text-[var(--text2)] hover:bg-[var(--surface)] hover:text-[var(--text)]"
              }`}
            >
              {renamingId === s.id ? (
                <form
                  className="min-w-0 flex-1"
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
                <button
                  className="min-w-0 flex-1 text-left"
                  onClick={() => {
                    select(s.id);
                    onClose?.();
                  }}
                >
                  <div className="truncate">{s.title || "Hội thoại mới"}</div>
                  <div className="text-[11px] text-[var(--text2)]">{relativeTime(s.updated_at)}</div>
                </button>
              )}
              <div className="hidden shrink-0 gap-1 group-hover:flex">
                <button
                  className="text-[11px] text-[var(--text2)] hover:text-[var(--text)]"
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
      </div>

      <div className="border-t border-[var(--border)]">
        {isAdmin && (
          <nav className="space-y-0.5 px-3 py-2">
            {ADMIN.map((it) => (
              <Item key={it.href} {...it} onNavigate={onClose} />
            ))}
          </nav>
        )}
        <div className="border-t border-[var(--border)] px-4 py-3">
          <div className="text-[11px] text-[var(--text2)]">{claims ? ROLE_LABEL[claims.role] : "—"}</div>
          <div className="mb-2 text-[11px] text-[var(--text2)]">
            Tenant:{" "}
            <code className="mono text-[var(--link)]">{claims?.tid ? claims.tid.slice(0, 8) + "…" : "-"}</code>
          </div>
          <button onClick={logout} className="text-[12px] text-[var(--link)] hover:underline">
            Đăng xuất
          </button>
        </div>
      </div>
    </aside>
  );
}
