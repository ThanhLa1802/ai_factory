"use client";

import { useCallback, useEffect, useState } from "react";
import ChatClient from "@/components/ChatClient";
import SessionsSidebar from "@/components/SessionsSidebar";
import { apiFetch } from "@/lib/api";
import type { SessionSummary } from "@/lib/types";

function newSessionId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export default function ChatPageClient() {
  const [activeId, setActiveId] = useState<string>(() => newSessionId());
  const [sessions, setSessions] = useState<SessionSummary[]>([]);

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

  return (
    <div className="flex h-full bg-[var(--bg)]">
      <SessionsSidebar
        sessions={sessions}
        activeId={activeId}
        onSelect={setActiveId}
        onNew={() => setActiveId(newSessionId())}
        onChanged={refresh}
      />
      <div className="min-w-0 flex-1">
        <ChatClient sessionId={activeId} onSessionChanged={refresh} />
      </div>
    </div>
  );
}
