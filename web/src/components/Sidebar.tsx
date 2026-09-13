"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import BrandMark from "@/components/BrandMark";
import ConfirmDialog from "@/components/ConfirmDialog";
import { useAuth } from "@/context/AuthContext";
import { useChatSessions } from "@/context/ChatSessionsContext";
import { isPlatformAdmin, ROLE_LABEL } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import type { Tenant } from "@/lib/types";

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
  if (m < 1) return "just now";
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d} d ago`;
  return new Date(iso).toLocaleDateString("en-US");
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

function PencilIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5" aria-hidden="true">
      <path d="M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z" />
    </svg>
  );
}

function TrashIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5" aria-hidden="true">
      <path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2m2 0v14a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V6" />
    </svg>
  );
}

export default function Sidebar({ open = false, onClose }: { open?: boolean; onClose?: () => void }) {
  const { claims, logout } = useAuth();
  const { sessions, activeId, select, newChat, refresh } = useChatSessions();
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const [tenantName, setTenantName] = useState<string | null>(null);
  const asideRef = useRef<HTMLElement | null>(null);

  const isAdmin = !!claims && isPlatformAdmin(claims.role);

  // Resolve the tenant's display name (roles with tenant.read). Falls back to the
  // short id below for roles that can't read the tenant list.
  const canReadTenants = claims?.role === "PLATFORM_ADMIN" || claims?.role === "TENANT_ADMIN";
  useEffect(() => {
    if (!claims?.tid || !canReadTenants) return;
    let cancelled = false;
    apiFetch<Tenant[]>("/api/v1/tenants")
      .then((ts) => {
        const t = (ts ?? []).find((x) => x.id === claims.tid);
        if (!cancelled && t) setTenantName(t.name);
      })
      .catch(() => {
        /* no permission / server down — keep the id fallback */
      });
    return () => {
      cancelled = true;
    };
  }, [claims?.tid, canReadTenants]);

  // Mobile drawer: move focus into the panel on open, close on Escape, and keep
  // Tab inside the panel while it is open. Suspended while the confirm modal is
  // up so the two traps don't fight.
  useEffect(() => {
    if (!open || confirmId) return;
    const el = asideRef.current;
    if (!el) return;
    const focusables = () =>
      Array.from(
        el.querySelectorAll<HTMLElement>(
          'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((n) => n.offsetParent !== null);
    focusables()[0]?.focus();

    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose?.();
        return;
      }
      if (e.key !== "Tab") return;
      const list = focusables();
      if (list.length === 0) return;
      const first = list[0];
      const last = list[list.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose, confirmId]);

  async function rename(id: string) {
    const title = draft.trim();
    if (!title) return;
    await apiFetch(`/api/v1/sessions/${id}`, { method: "PATCH", body: { title } });
    setRenamingId(null);
    refresh();
  }

  async function doRemove(id: string) {
    try {
      await apiFetch(`/api/v1/sessions/${id}`, { method: "DELETE" });
      if (id === activeId) newChat();
      refresh();
    } finally {
      setConfirmId(null);
    }
  }

  return (
    <aside
      ref={asideRef}
      role={open ? "dialog" : undefined}
      aria-modal={open ? true : undefined}
      aria-label={open ? "Navigation menu" : undefined}
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
          aria-label="Close menu"
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
          New chat
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
            <div className="px-2 py-6 text-center text-[12px] text-[var(--text2)]">No conversations yet.</div>
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
                    aria-label="Rename conversation"
                    className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-1 py-0.5 text-[13px]"
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
                  <div className="truncate">{s.title || "New conversation"}</div>
                  <div className="text-[11px] text-[var(--text2)]">{relativeTime(s.updated_at)}</div>
                </button>
              )}
              <div className="flex shrink-0 items-center gap-1">
                <button
                  aria-label="Rename conversation"
                  className="text-[var(--text2)] hover:text-[var(--text)]"
                  onClick={() => {
                    setRenamingId(s.id);
                    setDraft(s.title);
                  }}
                >
                  <PencilIcon />
                </button>
                <button
                  aria-label="Delete conversation"
                  className="text-[var(--text2)] hover:text-[var(--err)]"
                  onClick={() => setConfirmId(s.id)}
                >
                  <TrashIcon />
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
            <span className="mono text-[var(--link)]" title={claims?.tid || undefined}>
              {tenantName ?? (claims?.tid ? claims.tid.slice(0, 8) + "…" : "-")}
            </span>
          </div>
          <button onClick={logout} className="text-[12px] text-[var(--link)] hover:underline">
            Sign out
          </button>
        </div>
      </div>

      <ConfirmDialog
        open={!!confirmId}
        title="Delete this conversation?"
        message="The conversation and all its messages will be permanently deleted."
        confirmLabel="Delete"
        danger
        onConfirm={() => confirmId && doRemove(confirmId)}
        onCancel={() => setConfirmId(null)}
      />
    </aside>
  );
}
