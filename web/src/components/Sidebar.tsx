"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { useChatSessions } from "@/context/ChatSessionsContext";
import { isPlatformAdmin, ROLE_LABEL } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

const items = [
  { href: "/chat", label: "Chat", icon: "💬" },
  { href: "/platform", label: "Platform", icon: "📊" },
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

function Item({ href, label, icon }: { href: string; label: string; icon: string }) {
  const pathname = usePathname();
  const active = pathname.startsWith(href);
  return (
    <Link
      href={href}
      className={`flex items-center gap-3 px-4 py-2.5 text-[13px] ${
        active
          ? "bg-[var(--surface2)] text-white border-r-2 border-[var(--accent)]"
          : "text-[var(--text2)] hover:text-white hover:bg-[var(--surface2)]"
      }`}
    >
      <span>{icon}</span>
      {label}
    </Link>
  );
}

export default function Sidebar() {
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
    <aside className="flex w-56 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)]">
      <div className="border-b border-[var(--border)] px-4 py-4">
        <div className="text-[15px] font-semibold">AI Factory</div>
        <div className="text-[11px] text-[var(--text2)]">Inference Platform</div>
      </div>

      <nav className="py-2">
        {items.map((it) => (
          <Item key={it.href} {...it} />
        ))}
      </nav>

      {/* Lịch sử hội thoại — hiện ngay dưới Platform */}
      <div className="flex min-h-0 flex-1 flex-col border-t border-[var(--border)]">
        <div className="p-3">
          <button
            onClick={newChat}
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
                s.id === activeId
                  ? "bg-[var(--surface2)] text-white"
                  : "text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-white"
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
                <button className="min-w-0 flex-1 text-left" onClick={() => select(s.id)}>
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
      </div>

      {/* Nav dành riêng admin + footer */}
      <div className="border-t border-[var(--border)]">
        {isAdmin && (
          <nav className="py-2">
            <Item href="/infra" label="Infra" icon="⚙️" />
            <Item href="/admin" label="Admin" icon="🛡️" />
          </nav>
        )}
        <div className="border-t border-[var(--border)] px-4 py-3">
          <div className="text-[11px] text-[var(--text2)]">
            {claims ? ROLE_LABEL[claims.role] : "—"}
          </div>
          <div className="mb-2 text-[11px] text-[var(--text2)]">
            Tenant: <code className="text-[var(--link)]">{claims?.tid ? claims.tid.slice(0, 8) + "…" : "-"}</code>
          </div>
          <button onClick={logout} className="text-[12px] text-[var(--accent)] hover:underline">
            Đăng xuất
          </button>
        </div>
      </div>
    </aside>
  );
}
