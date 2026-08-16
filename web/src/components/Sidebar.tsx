"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAuth } from "@/context/AuthContext";
import { isPlatformAdmin, ROLE_LABEL } from "@/lib/auth";

const items = [
  { href: "/chat", label: "Chat", icon: "💬" },
  { href: "/keys", label: "API Keys", icon: "🔑" },
  { href: "/platform", label: "Platform", icon: "⚙️" },
];

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

  return (
    <aside className="flex w-56 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)]">
      <div className="border-b border-[var(--border)] px-4 py-4">
        <div className="text-[15px] font-semibold">AI Factory</div>
        <div className="text-[11px] text-[var(--text2)]">Inference Platform</div>
      </div>

      <nav className="flex-1 py-2">
        {items.map((it) => (
          <Item key={it.href} {...it} />
        ))}
        {claims && isPlatformAdmin(claims.role) && (
          <Item href="/admin" label="Admin" icon="🛡️" />
        )}
      </nav>

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
    </aside>
  );
}
