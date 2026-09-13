"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { newSessionId, useChatSessions } from "@/context/ChatSessionsContext";

// /chat with no session id: land on the most recent conversation (newest first
// per the server), or start a fresh one when there is no history yet.
export default function ChatIndex() {
  const router = useRouter();
  const { sessions, loaded } = useChatSessions();

  useEffect(() => {
    if (!loaded) return;
    router.replace(`/chat/${sessions[0]?.id || newSessionId()}`);
  }, [loaded, sessions, router]);

  return null;
}
