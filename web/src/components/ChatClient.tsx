"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import AuthImage from "@/components/AuthImage";
import BrandMark from "@/components/BrandMark";
import Markdown from "@/components/Markdown";
import { useAuth } from "@/context/AuthContext";
import { useChatSessions } from "@/context/ChatSessionsContext";
import { apiFetch } from "@/lib/api";
import type { Model } from "@/lib/types";

// One attached image. src is a local preview for the turn just sent; id is the
// persisted upload reference replayed from session history (fetched via auth).
interface ChatImage {
  id?: string;
  src?: string;
}

interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system" | "tool" | "error";
  content: string;
  images?: ChatImage[];
  reasoning?: string; // ephemeral reasoning/thinking text (not persisted)
  thinkingOpen?: boolean; // whether the thinking block is expanded
}

// An image picked in the composer but not sent yet. The data URL is what the
// OpenAI content part carries; the server persists the decoded bytes.
interface PendingImage {
  id: string;
  dataUrl: string;
  mime: string;
}

// Caps mirror the server (vision.max_images_per_message / max_image_bytes):
// four images, each resized client-side to ≤1024px JPEG (~2 MiB ceiling).
const MAX_IMAGES = 4;
const MAX_EDGE = 1024;
const MAX_INPUT_BYTES = 20 * 1024 * 1024;

// downscaleImage decodes a File, draws it into a ≤MAX_EDGE box and returns a
// JPEG data URL. Resizing client-side keeps uploads small and bounds the
// vision model's prompt-token cost.
async function downscaleImage(file: File): Promise<{ dataUrl: string; mime: string }> {
  const bitmap = await createImageBitmap(file);
  try {
    const scale = Math.min(1, MAX_EDGE / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("canvas is not supported");
    // JPEG has no alpha; paint white first so transparent PNGs don't go black.
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, width, height);
    ctx.drawImage(bitmap, 0, 0, width, height);
    return { dataUrl: canvas.toDataURL("image/jpeg", 0.85), mime: "image/jpeg" };
  } finally {
    bitmap.close();
  }
}

// Shape of a Go server SSE chunk (OpenAI-compatible stream).
interface SSEChunk {
  type?: string;
  error?: { message?: string };
  choices?: Array<{
    delta?: {
      content?: string;
      reasoning_content?: string;
      tool_calls?: Array<{ function?: { name?: string; arguments?: string } }>;
    };
  }>;
}

function newId() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "s-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function UserAvatar() {
  return (
    <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--surface2)]">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="h-4 w-4 text-[var(--text2)]"
        aria-hidden="true"
      >
        <path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 20a8 8 0 0 1 16 0" />
      </svg>
    </div>
  );
}

export default function ChatClient() {
  const { token } = useAuth();
  const { activeId, refresh } = useChatSessions();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [showSystem, setShowSystem] = useState(false);
  const [models, setModels] = useState<string[]>([]);
  const [model, setModel] = useState("");
  const [modelsLoaded, setModelsLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [stats, setStats] = useState<{ tokens: number; ms: number } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pendingImages, setPendingImages] = useState<PendingImage[]>([]);

  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);
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
    setPendingImages([]);
    if (!activeId) return;
    apiFetch<{
      title: string;
      model: string;
      messages: Array<{
        role: string;
        content: string;
        tool_result?: string;
        tool_calls?: Array<{ name: string; arguments: string }>;
        is_error?: boolean;
        images?: Array<{ id: string; mime_type: string }>;
      }>;
    }>(`/api/v1/sessions/${activeId}`)
      .then((data) => {
        if (cancelled) return;
        const mapped: ChatMessage[] = (data.messages || []).map((m) => {
          if (m.role === "tool") {
            return {
              id: newId(),
              role: "tool",
              content: `Tool result: ${m.tool_result || "(empty)"}`,
            };
          }
          return {
            id: newId(),
            role: m.role as ChatMessage["role"],
            content: m.content,
            images: (m.images || []).map((img) => ({ id: img.id })),
          };
        });
        if (mapped.length) setMessages(mapped);
      })
      .catch(() => {
        // 404 = this session does not exist yet — keep the chat empty.
      });
    return () => {
      cancelled = true;
    };
  }, [activeId]);

  // Load the models that can actually serve (READY deployment) for the
  // selector, so catalog leftovers with no deployment never show up here.
  useEffect(() => {
    let cancelled = false;
    apiFetch<Model[]>("/api/v1/models?ready=true")
      .then((ms) => {
        if (cancelled) return;
        const names = (ms ?? []).map((m) => m.name);
        if (names.length) {
          setModels(names);
          setModel(names.find((n) => n === "qwen-3b") || names.find((n) => n === "qwen3.5-9b") || names[0]);
        }
      })
      .catch(() => {
        /* no model.read permission or server down — keep default */
      })
      .finally(() => {
        if (!cancelled) setModelsLoaded(true);
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

  // Auto-grow the composer textarea up to a cap.
  useEffect(() => {
    const el = taRef.current;
    if (el) {
      el.style.height = "auto";
      el.style.height = Math.min(el.scrollHeight, 200) + "px";
    }
  }, [input]);

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

  function patchMessage(id: string, patch: Partial<ChatMessage>) {
    setMessages((prev) => prev.map((m) => (m.id === id ? { ...m, ...patch } : m)));
  }

  // addFiles resizes and queues picked images. It also switches to a vision
  // model when one is registered, so attaching an image to a text model does not
  // fail at the worker with "image input requires --engine vl".
  async function addFiles(files: FileList | null) {
    if (!files?.length) return;
    setError(null);

    const room = MAX_IMAGES - pendingImages.length;
    if (room <= 0) {
      setError(`You can attach at most ${MAX_IMAGES} images per message.`);
      return;
    }
    const picked = Array.from(files).slice(0, room);
    if (files.length > room) {
      setError(`Only ${MAX_IMAGES} images per message — extra files were ignored.`);
    }

    const next: PendingImage[] = [];
    for (const file of picked) {
      if (file.type !== "image/png" && file.type !== "image/jpeg") {
        setError("Only PNG and JPEG images are supported.");
        continue;
      }
      if (file.size > MAX_INPUT_BYTES) {
        setError(`${file.name} is too large (max ${MAX_INPUT_BYTES / (1024 * 1024)} MB).`);
        continue;
      }
      try {
        const { dataUrl, mime } = await downscaleImage(file);
        next.push({ id: newId(), dataUrl, mime });
      } catch {
        setError(`Could not read ${file.name}.`);
      }
    }

    if (!next.length) return;
    setPendingImages((prev) => [...prev, ...next]);
    if (!/-vl/i.test(model)) {
      const visionModel = models.find((n) => /-vl/i.test(n));
      if (visionModel) setModel(visionModel);
    }
  }

  function removePendingImage(id: string) {
    setPendingImages((prev) => prev.filter((img) => img.id !== id));
  }

  const send = useCallback(async () => {
    const text = input.trim();
    const images = pendingImages;
    if ((!text && images.length === 0) || busy || !model) return;
    setInput("");
    setError(null);
    setStats(null);
    setPendingImages([]);

    pushMsg({
      id: newId(),
      role: "user",
      content: text,
      images: images.map((img) => ({ src: img.dataUrl })),
    });
    pushMsg({ id: newId(), role: "assistant", content: "" });

    const ac = new AbortController();
    abortRef.current = ac;
    liveStreamRef.current = activeId;
    setBusy(true);

    let fullText = "";
    let reasoningText = "";
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
          max_tokens: 2048,
          messages: [
            ...(systemPrompt.trim() ? [{ role: "system", content: systemPrompt.trim() }] : []),
            {
              role: "user",
              // Text-only turns keep the plain-string shape; images use OpenAI
              // content parts (inline base64 data URLs — no remote fetch).
              content:
                images.length > 0
                  ? [
                      ...(text ? [{ type: "text", text }] : []),
                      ...images.map((img) => ({ type: "image_url", image_url: { url: img.dataUrl } })),
                    ]
                  : text,
            },
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
        if (resp.status === 404) {
          msg = "No READY deployment for this model — open /infra to create one and wait for READY.";
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
            fullText += `\n\n> **Error:** ${msg}`;
            updateLastAssistant({ content: fullText });
            continue;
          }

          const choice = ev?.choices?.[0];
          if (!choice) continue;

          const delta = choice.delta || {};
          if (typeof delta.reasoning_content === "string" && delta.reasoning_content) {
            reasoningText += delta.reasoning_content;
            updateLastAssistant({ reasoning: reasoningText, thinkingOpen: true });
          }
          if (typeof delta.content === "string" && delta.content) {
            fullText += delta.content;
            tokenCount++;
            // First content token → auto-collapse the thinking block.
            updateLastAssistant({ content: fullText, thinkingOpen: false });
          }
          if (Array.isArray(delta.tool_calls)) {
            for (const tc of delta.tool_calls) {
              const fn = tc?.function;
              if (fn?.name) {
                const args = (fn.arguments || "").replace(/\s+/g, " ").slice(0, 120);
                fullText += `\n\n> **Tool** \`${fn.name}(${args})\`\n\n`;
                updateLastAssistant({ content: fullText });
              }
            }
          }
        }
      }

      if (abortRef.current === ac) setStats({ tokens: tokenCount, ms: Math.round(performance.now() - start) });
    } catch (err: unknown) {
      const name = err instanceof Error ? err.name : "";
      const message = err instanceof Error ? err.message : "Unknown error";
      if (name === "AbortError") {
        updateLastAssistant({ content: fullText + "\n\n> **Stopped.**" });
      } else {
        setError(message);
        updateLastAssistant({ content: fullText + `\n\n> **Error:** ${message}` });
      }
    } finally {
      if (abortRef.current === ac) {
        abortRef.current = null;
        liveStreamRef.current = null;
        setBusy(false);
      }
      refresh();
    }
  }, [input, busy, model, systemPrompt, token, activeId, refresh, pendingImages]);

  function stop() {
    abortRef.current?.abort();
  }

  return (
    <div className="flex h-full flex-col">
      {/* messages */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[46rem] px-4 py-6">
          {messages.length === 0 ? (
            <div className="flex min-h-[60vh] flex-col items-center justify-center gap-4 text-center">
              <BrandMark size={44} />
              <h1 className="text-2xl font-semibold tracking-tight">What would you like to ask today?</h1>
              <p className="max-w-sm text-[14px] leading-relaxed text-[var(--text2)]">
                Chat with the model over an OpenAI-compatible API. The server keeps history per session.
              </p>
            </div>
          ) : (
            messages.map((m, idx) => {
              const isLast = idx === messages.length - 1;

              if (m.role === "tool") {
                return (
                  <div key={m.id} className="py-1">
                    <div className="ml-10 border-l-2 border-[var(--border)] py-1 pl-3 text-[13px] text-[var(--text2)]">
                      {m.content}
                    </div>
                  </div>
                );
              }

              if (m.role === "error") {
                return (
                  <div key={m.id} className="py-2 text-[13px] text-[var(--err)]">
                    {m.content}
                  </div>
                );
              }

              if (m.role === "user") {
                return (
                  <div key={m.id} className="flex items-start justify-end gap-3 py-4">
                    <div className="flex max-w-[80%] flex-col items-end gap-2">
                      {m.images && m.images.length > 0 && (
                        <div className="flex flex-wrap justify-end gap-2">
                          {m.images.map((img, i) =>
                            img.src ? (
                              // eslint-disable-next-line @next/next/no-img-element -- local data: URL preview
                              <img
                                key={i}
                                src={img.src}
                                alt="Attached image"
                                className="max-h-48 rounded-xl border border-[var(--border)] object-cover"
                              />
                            ) : img.id ? (
                              <AuthImage
                                key={i}
                                id={img.id}
                                alt="Attached image"
                                className="min-h-16 min-w-16 max-h-48 rounded-xl border border-[var(--border)] object-cover"
                              />
                            ) : null,
                          )}
                        </div>
                      )}
                      {m.content && (
                        <div className="whitespace-pre-wrap text-[15px] leading-relaxed">{m.content}</div>
                      )}
                    </div>
                    <UserAvatar />
                  </div>
                );
              }

              // assistant (and system fallback)
              return (
                <div key={m.id} className="flex items-start gap-3 py-4">
                  <BrandMark size={28} />
                  <div className="min-w-0 flex-1">
                    <div className="mb-1 flex items-baseline gap-2">
                      <span className="text-[14px] font-semibold">AI Factory</span>
                      {isLast && stats && (
                        <span className="mono text-[11px] text-[var(--text2)]">
                          {stats.tokens} tok · {stats.ms}ms
                        </span>
                      )}
                    </div>

                    {m.reasoning ? (
                      <div className="mb-3">
                        <button
                          type="button"
                          onClick={() => patchMessage(m.id, { thinkingOpen: !m.thinkingOpen })}
                          className="flex items-center gap-1.5 text-[12px] font-medium text-[var(--text2)] hover:text-[var(--text)]"
                        >
                          <span className="inline-block w-3 text-center leading-none">{m.thinkingOpen ? "▾" : "▸"}</span>
                          Thinking
                        </button>
                        {m.thinkingOpen ? (
                          <div className="mt-2 border-l-2 border-[var(--border)] pl-3 text-[13px] leading-relaxed text-[var(--text2)]">
                            <Markdown content={m.reasoning} />
                          </div>
                        ) : null}
                      </div>
                    ) : null}

                    {m.content ? (
                      <div className={busy && isLast ? "streaming" : ""}>
                        <Markdown content={m.content} />
                      </div>
                    ) : busy && isLast ? (
                      <div className="pulse text-[14px] text-[var(--text2)]">Thinking…</div>
                    ) : null}
                  </div>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* composer */}
      <div className="border-t border-[var(--border)] bg-[var(--bg)]">
        <div className="mx-auto w-full max-w-[46rem] px-4 py-3">
          <div className="mb-2 flex items-center gap-2">
            <select
              value={model}
              onChange={(e) => setModel(e.target.value)}
              className="rounded-lg border border-[var(--border)] bg-transparent px-2.5 py-1.5 text-[12px] text-[var(--text2)] outline-none focus:border-[var(--accent)]"
            >
              {models.map((m) => (
                <option key={m} value={m} className="bg-[var(--bg2)]">
                  {m}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => setShowSystem((v) => !v)}
              title="System prompt"
              aria-label="System prompt"
              aria-pressed={showSystem}
              className={`flex h-7 w-7 items-center justify-center rounded-lg text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-[var(--text)] ${
                showSystem ? "text-[var(--text)]" : ""
              }`}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4" aria-hidden="true">
                <path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6" />
              </svg>
            </button>
          </div>

          {modelsLoaded && models.length === 0 && (
            <div className="mb-2 rounded-lg border border-[var(--warn)]/40 bg-[var(--warn)]/10 px-3 py-2 text-[13px] text-[var(--warn)]">
              No models yet — go to{" "}
              <a href="/infra" className="underline">
                /infra
              </a>{" "}
              to create a model + READY deployment before chatting.
            </div>
          )}

          {showSystem && (
            <input
              value={systemPrompt}
              onChange={(e) => setSystemPrompt(e.target.value)}
              placeholder="System prompt (optional) — define how the model should respond"
              className="mb-2 w-full rounded-lg border border-[var(--border)] bg-transparent px-3 py-1.5 text-[13px] outline-none placeholder:text-[var(--text2)] focus:border-[var(--accent)]"
            />
          )}

          {error && (
            <div role="alert" className="mb-2 rounded-lg border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
              {error}
            </div>
          )}

          {pendingImages.length > 0 && (
            <div className="mb-2 flex flex-wrap gap-2">
              {pendingImages.map((img) => (
                <div key={img.id} className="relative">
                  {/* eslint-disable-next-line @next/next/no-img-element -- local data: URL preview */}
                  <img
                    src={img.dataUrl}
                    alt="Attachment preview"
                    className="h-16 w-16 rounded-lg border border-[var(--border)] object-cover"
                  />
                  <button
                    type="button"
                    onClick={() => removePendingImage(img.id)}
                    aria-label="Remove attachment"
                    title="Remove attachment"
                    className="absolute -right-1.5 -top-1.5 flex h-5 w-5 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--bg2)] text-[11px] leading-none text-[var(--text2)] hover:text-[var(--text)]"
                  >
                    ×
                  </button>
                </div>
              ))}
            </div>
          )}

          <form
            onSubmit={(e) => {
              e.preventDefault();
              send();
            }}
            className="flex items-end gap-2 rounded-2xl border border-[var(--border)] bg-[var(--bg2)] p-2 transition-colors focus-within:border-[var(--accent)]"
          >
            <input
              ref={fileRef}
              type="file"
              accept="image/png,image/jpeg"
              multiple
              hidden
              onChange={(e) => {
                addFiles(e.target.files);
                e.target.value = "";
              }}
            />
            <button
              type="button"
              onClick={() => fileRef.current?.click()}
              disabled={busy || pendingImages.length >= MAX_IMAGES}
              aria-label="Attach image"
              title="Attach image"
              className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-[var(--text)] disabled:cursor-not-allowed disabled:opacity-40"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4" aria-hidden="true">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
              </svg>
            </button>
            <textarea
              ref={taRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  send();
                }
              }}
              rows={1}
              placeholder="Message AI Factory…"
              className="max-h-[200px] min-h-[24px] flex-1 resize-none bg-transparent px-3 py-2 text-[15px] leading-relaxed outline-none placeholder:text-[var(--text2)]"
            />
            {busy ? (
              <button
                type="button"
                onClick={stop}
                aria-label="Stop"
                className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--surface2)] text-[var(--text)] hover:bg-[var(--surface)]"
              >
                <span className="h-3 w-3 rounded-[2px] bg-current" />
              </button>
            ) : (
              <button
                type="submit"
                disabled={(!input.trim() && pendingImages.length === 0) || !model}
                aria-label="Send"
                className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-[var(--accent-strong)] text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4" aria-hidden="true">
                  <path d="M12 19V5M5 12l7-7 7 7" />
                </svg>
              </button>
            )}
          </form>

          <div className="mt-1.5 text-center text-[11px] text-[var(--text2)]">
            AI Factory can make mistakes. Check important information.
          </div>
        </div>
      </div>
    </div>
  );
}
