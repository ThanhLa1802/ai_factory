"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useRouter } from "next/navigation";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

function newSessionId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

interface ChatSessionsState {
  sessions: SessionSummary[];
  activeId: string;
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
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [activeId, setActiveId] = useState<string>(() => newSessionId());

  const refresh = useCallback(async () => {
    try {
      setSessions(await apiFetch<SessionSummary[]>("/api/v1/sessions"));
    } catch {
      /* server down — keep the current list */
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    refresh();
  }, [refresh]);

  const select = useCallback(
    (id: string) => {
      setActiveId(id);
      router.push("/chat");
    },
    [router],
  );

  const newChat = useCallback(() => {
    setActiveId(newSessionId());
    router.push("/chat");
  }, [router]);

  const value = useMemo(
    () => ({ sessions, activeId, select, newChat, refresh }),
    [sessions, activeId, select, newChat, refresh],
  );

  return <ChatSessionsContext.Provider value={value}>{children}</ChatSessionsContext.Provider>;
}

export function useChatSessions(): ChatSessionsState {
  const ctx = useContext(ChatSessionsContext);
  if (!ctx) throw new Error("useChatSessions must be used within ChatSessionsProvider");
  return ctx;
}
