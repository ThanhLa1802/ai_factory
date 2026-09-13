"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

export function newSessionId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

// The active session id lives in the URL (/chat/<id>), so refresh, back/forward,
// and sharing a link all restore the same conversation instead of spawning a new
// empty one on every mount.
function sessionIdFromPath(pathname: string | null): string {
  const match = pathname?.match(/^\/chat\/([^/?#]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

interface ChatSessionsState {
  sessions: SessionSummary[];
  activeId: string;
  loaded: boolean;
  select: (id: string) => void;
  newChat: () => void;
  refresh: () => void;
}

const ChatSessionsContext = createContext<ChatSessionsState | undefined>(undefined);

// Lifts the session list + active-session state out of the /chat page so the
// global sidebar can render it (history lives right below "Platform", on every
// page) and ChatClient can read the active id from the same source.
export function ChatSessionsProvider({ children }: { children: ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [loaded, setLoaded] = useState(false);

  const activeId = useMemo(() => sessionIdFromPath(pathname), [pathname]);

  const refresh = useCallback(async () => {
    try {
      setSessions((await apiFetch<SessionSummary[]>("/api/v1/sessions")) ?? []);
    } catch {
      /* server down — keep the current list */
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    refresh();
  }, [refresh]);

  const select = useCallback(
    (id: string) => {
      if (id) router.push(`/chat/${id}`);
    },
    [router],
  );

  const newChat = useCallback(() => {
    router.push(`/chat/${newSessionId()}`);
  }, [router]);

  const value = useMemo(
    () => ({ sessions, activeId, loaded, select, newChat, refresh }),
    [sessions, activeId, loaded, select, newChat, refresh],
  );

  return <ChatSessionsContext.Provider value={value}>{children}</ChatSessionsContext.Provider>;
}

export function useChatSessions(): ChatSessionsState {
  const ctx = useContext(ChatSessionsContext);
  if (!ctx) throw new Error("useChatSessions must be used within ChatSessionsProvider");
  return ctx;
}
