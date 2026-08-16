"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Markdown from "@/components/Markdown";
import { useAuth } from "@/context/AuthContext";
import { useChatSessions } from "@/context/ChatSessionsContext";
import { apiFetch } from "@/lib/api";
import type { Model } from "@/lib/types";

interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system" | "tool" | "error";
  content: string;
}

// Shape of a Go server SSE chunk (OpenAI-compatible stream).
interface SSEChunk {
  type?: string;
  error?: { message?: string };
  choices?: Array<{
    delta?: {
      content?: string;
      tool_calls?: Array<{ function?: { name?: string; arguments?: string } }>;
    };
  }>;
}

function newId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function avatarFor(role: ChatMessage["role"]): string {
  switch (role) {
    case "user":
      return "👤";
    case "tool":
      return "🔧";
    case "error":
      return "⚠️";
    default:
      return "🤖"; // assistant + system
  }
}

export default function ChatClient() {
  const { token } = useAuth();
  const { activeId, refresh } = useChatSessions();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [models, setModels] = useState<string[]>(["qwen-3b"]);
  const [model, setModel] = useState("qwen-3b");
  const [busy, setBusy] = useState(false);
  const [stats, setStats] = useState<{ tokens: number; ms: number } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const activeIdRef = useRef(activeId);
  const liveStreamRef = useRef<string | null>(null);

  // Keep activeIdRef fresh so the async stream loop can compare against the
  // currently-selected session and drop stale writes after a mid-stream switch.
  useEffect(() => {
    activeIdRef.current = activeId;
  }, [activeId]);

  // Abort an in-flight stream when the active session changes (or the component
  // unmounts), so a previous turn can't keep writing into the new conversation.
  useEffect(() => {
    return () => {
      abortRef.current?.abort();
    };
  }, [activeId]);

  useEffect(() => {
    let cancelled = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset messages when switching sessions
    setMessages([]);
    apiFetch<{ title: string; model: string; messages: Array<{ role: string; content: string; tool_result?: string; tool_calls?: Array<{ name: string; arguments: string }>; is_error?: boolean }> }>(
      `/api/v1/sessions/${activeId}`,
    )
      .then((data) => {
        if (cancelled) return;
        const mapped: ChatMessage[] = (data.messages || []).map((m) => {
          if (m.role === "tool") {
            return {
              id: newId(),
              role: "tool",
              content: `🔧 Tool result: ${m.tool_result || "(empty)"}`,
            };
          }
          return { id: newId(), role: m.role as ChatMessage["role"], content: m.content };
        });
        if (mapped.length) setMessages(mapped);
      })
      .catch(() => {
        // 404 = chưa có session này — giữ chat trống.
      });
    return () => {
      cancelled = true;
    };
  }, [activeId]);

  // Load model registry for the selector (fall back to the seeded qwen-3b).
  useEffect(() => {
    let cancelled = false;
    apiFetch<Model[]>("/api/v1/models")
      .then((ms) => {
        if (cancelled) return;
        const names = ms.map((m) => m.name);
        if (names.length) {
          setModels(names);
          setModel(names.find((n) => n === "qwen-3b") || names[0]);
        }
      })
      .catch(() => {
        /* no model.read permission or server down — keep default */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Autoscroll to the bottom on new content.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, busy]);

  function pushMsg(m: ChatMessage) {
    setMessages((prev) => [...prev, m]);
  }

  function updateLastAssistant(patch: Partial<ChatMessage>) {
    // Drop updates from a stream whose session is no longer active (mid-stream switch).
    if (liveStreamRef.current !== activeIdRef.current) return;
    setMessages((prev) => {
      const next = [...prev];
      for (let i = next.length - 1; i >= 0; i--) {
        if (next[i].role === "assistant") {
          next[i] = { ...next[i], ...patch };
          return next;
        }
      }
      return next;
    });
  }

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || busy) return;
    setInput("");
    setError(null);
    setStats(null);

    pushMsg({ id: newId(), role: "user", content: text });
    pushMsg({ id: newId(), role: "assistant", content: "" });

    const ac = new AbortController();
    abortRef.current = ac;
    liveStreamRef.current = activeId;
    setBusy(true);

    let fullText = "";
    let tokenCount = 0;
    const start = performance.now();

    try {
      const resp = await fetch("/v1/chat/completions", {
        method: "POST",
        signal: ac.signal,
        headers: {
          "Content-Type": "application/json",
          "x-session-id": activeId,
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          model,
          stream: true,
          max_tokens: 1024,
          messages: [
            ...(systemPrompt.trim() ? [{ role: "system", content: systemPrompt.trim() }] : []),
            { role: "user", content: text },
          ],
        }),
      });

      if (!resp.ok) {
        let msg = resp.statusText;
        try {
          const err = await resp.json();
          msg = err?.error?.message || msg;
        } catch {
          /* non-JSON */
        }
        throw new Error(msg);
      }

      const reader = resp.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const lines = buffer.split("\n");
        buffer = lines.pop() || "";

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed.startsWith("data:")) continue;
          const data = trimmed.slice(5).trim();
          if (data === "[DONE]") continue;

          let ev: SSEChunk;
          try {
            ev = JSON.parse(data) as SSEChunk;
          } catch {
            continue;
          }

          // Server-side error frame.
          if (ev.type === "error") {
            const msg = ev?.error?.message || "inference error";
            fullText += `\n\n> ⚠️ ${msg}`;
            updateLastAssistant({ content: fullText });
            continue;
          }

          const choice = ev?.choices?.[0];
          if (!choice) continue;

          const delta = choice.delta || {};
          if (typeof delta.content === "string" && delta.content) {
            fullText += delta.content;
            tokenCount++;
            updateLastAssistant({ content: fullText });
          }
          if (Array.isArray(delta.tool_calls)) {
            for (const tc of delta.tool_calls) {
              const fn = tc?.function;
              if (fn?.name) {
                const args = (fn.arguments || "").replace(/\s+/g, " ").slice(0, 120);
                fullText += `\n\n> 🔧 \`${fn.name}(${args})\`\n\n`;
                updateLastAssistant({ content: fullText });
              }
            }
          }
        }
      }

      if (abortRef.current === ac) setStats({ tokens: tokenCount, ms: Math.round(performance.now() - start) });
    } catch (err: unknown) {
      const name = err instanceof Error ? err.name : "";
      const message = err instanceof Error ? err.message : "Lỗi không xác định";
      if (name === "AbortError") {
        updateLastAssistant({ content: fullText + "\n\n> ⏹️ Đã dừng." });
      } else {
        setError(message);
        updateLastAssistant({ content: fullText + `\n\n> ❌ ${message}` });
      }
    } finally {
      if (abortRef.current === ac) {
        abortRef.current = null;
        liveStreamRef.current = null;
        setBusy(false);
      }
      refresh();
    }
  }, [input, busy, model, systemPrompt, token, activeId, refresh]);

  function stop() {
    abortRef.current?.abort();
  }

  return (
    <div className="flex h-full flex-col">
      {/* toolbar */}
      <div className="flex flex-wrap items-center gap-3 border-b border-[var(--border)] px-4 py-2.5">
        <label className="flex items-center gap-2 text-[12px] text-[var(--text2)]">
          Model
          <select
            value={model}
            onChange={(e) => setModel(e.target.value)}
            className="rounded-md border border-[var(--border)] bg-[var(--bg2)] px-2 py-1 text-[13px] outline-none focus:border-[var(--accent)]"
          >
            {models.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className="flex min-w-0 flex-1 items-center gap-2 text-[12px] text-[var(--text2)]">
          System prompt
          <input
            value={systemPrompt}
            onChange={(e) => setSystemPrompt(e.target.value)}
            placeholder="(tuỳ chọn)"
            className="min-w-0 flex-1 rounded-md border border-[var(--border)] bg-[var(--bg2)] px-2 py-1 text-[13px] outline-none focus:border-[var(--accent)]"
          />
        </label>
        {stats && (
          <span className="text-[12px] text-[var(--text2)]">
            ⚡ {stats.tokens} tok · {stats.ms}ms
          </span>
        )}
      </div>

      {/* messages */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-4">
        {messages.length === 0 && (
          <div className="mt-16 text-center text-[13px] text-[var(--text2)]">
            <div className="mb-1 text-2xl">💬</div>
            Chat với model qua giao diện OpenAI-compatible. Server giữ lịch sử theo{" "}
            <code className="text-[var(--link)]">x-session-id</code>.
          </div>
        )}
        <div className="flex max-w-4xl flex-col gap-4">
          {messages.map((m) => (
            <div
              key={m.id}
              className={`flex items-start gap-3 ${m.role === "user" ? "flex-row-reverse" : "flex-row"}`}
            >
              <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--surface2)] text-[13px] leading-none">
                {avatarFor(m.role)}
              </div>
              <div
                className={`max-w-[85%] rounded-xl px-4 py-2.5 ${
                  m.role === "user"
                    ? "bg-[var(--accent)] text-white"
                    : m.role === "error"
                      ? "border border-[var(--err)]/40 bg-[var(--err)]/10 text-[var(--err)]"
                      : m.role === "tool"
                        ? "border border-[var(--border)] bg-[var(--surface2)] text-[var(--text2)]"
                        : "border border-[var(--border)] bg-[var(--surface)]"
                } ${busy && m.role === "assistant" && m.id === messages[messages.length - 1]?.id ? "streaming" : ""}`}
              >
                {m.role === "assistant" ? (
                  <Markdown content={m.content} />
                ) : (
                  <div className="whitespace-pre-wrap text-[14px]">{m.content}</div>
                )}
              </div>
            </div>
          ))}
          {busy && (
            <div className="flex items-center gap-3">
              <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--surface2)] text-[13px] leading-none">
                🤖
              </div>
              <span className="animate-pulse text-[var(--text2)]">▍</span>
            </div>
          )}
        </div>
      </div>

      {/* error banner */}
      {error && (
        <div className="mx-4 mb-2 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
          {error}
        </div>
      )}

      {/* input */}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          send();
        }}
        className="flex items-end gap-2 border-t border-[var(--border)] px-4 py-3"
      >
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              send();
            }
          }}
          rows={1}
          placeholder="Nhập tin nhắn… (Shift+Enter để xuống dòng)"
          className="max-h-40 min-h-[42px] flex-1 resize-none rounded-lg border border-[var(--border)] bg-[var(--bg2)] px-3 py-2.5 text-[14px] outline-none focus:border-[var(--accent)]"
        />
        {busy ? (
          <button
            type="button"
            onClick={stop}
            className="h-[42px] rounded-lg border border-[var(--border)] px-4 text-[13px] text-[var(--text2)] hover:bg-[var(--surface2)]"
          >
            ⏹ Stop
          </button>
        ) : (
          <button
            type="submit"
            disabled={!input.trim()}
            className="h-[42px] rounded-lg bg-[var(--accent)] px-4 text-[14px] font-medium text-white hover:opacity-90 disabled:opacity-40"
          >
            Gửi
          </button>
        )}
      </form>
    </div>
  );
}
