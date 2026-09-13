"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Sidebar from "@/components/Sidebar";
import { useAuth } from "@/context/AuthContext";
import { ChatSessionsProvider } from "@/context/ChatSessionsContext";

// AuthGate: all /chat, /platform, /infra, /admin routes require a signed-in JWT.
export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { token, hydrated } = useAuth();
  const router = useRouter();
  const [navOpen, setNavOpen] = useState(false);
  const navToggleRef = useRef<HTMLButtonElement | null>(null);

  // Close the mobile drawer and return focus to its trigger.
  const closeNav = useCallback(() => {
    setNavOpen(false);
    navToggleRef.current?.focus();
  }, []);

  useEffect(() => {
    // Wait for hydration: before it, token is always null (no localStorage on the
    // server) and redirecting here would bounce every refresh to /login.
    if (hydrated && !token) router.replace("/login");
  }, [hydrated, token, router]);

  if (!hydrated || !token) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--bg)] text-[var(--text2)]">
        Loading…
      </div>
    );
  }

  return (
    <ChatSessionsProvider>
      <div className="flex h-screen">
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:left-3 focus:top-3 focus:z-50 focus:rounded-md focus:bg-[var(--surface2)] focus:px-3 focus:py-2 focus:text-[13px] focus:text-[var(--text)]"
        >
          Skip to content
        </a>
        <Sidebar open={navOpen} onClose={closeNav} />
        {navOpen && (
          <div
            onClick={closeNav}
            aria-hidden="true"
            className="fixed inset-0 z-30 bg-black/50 md:hidden"
          />
        )}
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex items-center gap-2 border-b border-[var(--border)] px-3 py-2 md:hidden">
            <button
              ref={navToggleRef}
              onClick={() => setNavOpen(true)}
              aria-label="Open menu"
              aria-expanded={navOpen}
              className="flex h-8 w-8 items-center justify-center rounded-lg text-[var(--text2)] hover:bg-[var(--surface)] hover:text-[var(--text)]"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-4 w-4" aria-hidden="true">
                <path d="M4 6h16M4 12h16M4 18h16" />
              </svg>
            </button>
            <span className="text-[13px] font-semibold">AI Factory</span>
          </div>
          <main id="main-content" className="min-w-0 flex-1 overflow-y-auto">{children}</main>
        </div>
      </div>
    </ChatSessionsProvider>
  );
}
