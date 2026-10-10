"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "@/lib/api";
import { getToken } from "@/lib/auth";
import type { ImageAsset, ImageJob } from "@/lib/types";

// SDXL-friendly sizes (multiples of 8, native 1024).
const SIZES = ["1024x1024", "1152x896", "896x1152", "768x768"];

interface GeneratedImage {
  src: string;
  seed: number;
  width: number;
  height: number;
  steps: number;
}

export default function ImageGenerator() {
  const [model, setModel] = useState("sdxl-1.0");
  const [prompt, setPrompt] = useState("");
  const [negative, setNegative] = useState("");
  const [size, setSize] = useState(SIZES[0]);
  const [steps, setSteps] = useState(25);
  const [guidance, setGuidance] = useState(7);
  const [seed, setSeed] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [images, setImages] = useState<GeneratedImage[]>([]);
  const [job, setJob] = useState<ImageJob | null>(null);

  const [assets, setAssets] = useState<ImageAsset[]>([]);
  const [assetUrls, setAssetUrls] = useState<Record<string, string>>({});
  const urlsRef = useRef<string[]>([]);
  const abortRef = useRef<AbortController | null>(null);

  const loadAssets = useCallback(async (): Promise<ImageAsset[]> => {
    try {
      const resp = await apiFetch<{ data: ImageAsset[] }>("/api/v1/images/assets?limit=12");
      setAssets(resp.data ?? []);
      return resp.data ?? [];
    } catch {
      // The gallery is best-effort; a failure must not block generation.
      return [];
    }
  }, []);

  // Assets are served behind auth, so <img src> cannot load them directly:
  // fetch the PNG as an authenticated blob and hand React an object URL.
  const fetchAssetUrl = useCallback(async (id: string): Promise<string | null> => {
    try {
      const token = getToken();
      const resp = await fetch(`/api/v1/images/assets/${id}`, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!resp.ok) return null;
      const url = URL.createObjectURL(await resp.blob());
      urlsRef.current.push(url);
      return url;
    } catch {
      return null;
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch
    loadAssets();
  }, [loadAssets]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      for (const a of assets) {
        if (assetUrls[a.id]) continue;
        const url = await fetchAssetUrl(a.id);
        if (!url) continue;
        if (cancelled) {
          URL.revokeObjectURL(url);
          return;
        }
        setAssetUrls((m) => ({ ...m, [a.id]: url }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [assets, assetUrls, fetchAssetUrl]);

  useEffect(
    () => () => {
      abortRef.current?.abort();
      urlsRef.current.forEach((u) => URL.revokeObjectURL(u));
    },
    [],
  );

  // streamJob consumes the SSE progress stream for a job and invokes onJob for
  // each snapshot. Resolves at [DONE] or when the server closes the stream.
  async function streamJob(
    id: string,
    onJob: (j: ImageJob) => void,
    signal: AbortSignal,
  ): Promise<void> {
    const token = getToken();
    const resp = await fetch(`/api/v1/images/jobs/${id}/events`, {
      headers: {
        Accept: "text/event-stream",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      signal,
    });
    if (!resp.ok || !resp.body) {
      throw new Error(`progress stream failed (${resp.status})`);
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buffer += decoder.decode(value, { stream: true });
      let sep: number;
      while ((sep = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, sep);
        buffer = buffer.slice(sep + 2);
        for (const line of frame.split("\n")) {
          if (!line.startsWith("data:")) continue;
          const data = line.slice(5).trim();
          if (data === "[DONE]") return;
          try {
            onJob(JSON.parse(data) as ImageJob);
          } catch {
            // ignore malformed frames
          }
        }
      }
    }
  }

  async function generate(e: React.FormEvent) {
    e.preventDefault();
    if (!prompt.trim() || loading) return;
    setLoading(true);
    setError(null);
    setImages([]);
    setJob(null);
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const body: Record<string, unknown> = {
        model: model.trim(),
        prompt: prompt.trim(),
        size,
        steps,
        guidance_scale: guidance,
      };
      if (negative.trim()) body.negative_prompt = negative.trim();
      if (seed.trim() !== "") body.seed = Number(seed);

      let latest = await apiFetch<ImageJob>("/api/v1/images/jobs", { method: "POST", body });
      setJob(latest);
      await streamJob(
        latest.id,
        (j) => {
          latest = j;
          setJob(j);
        },
        controller.signal,
      );

      if (latest.status === "failed") {
        setError(latest.error || "Generation failed");
        return;
      }
      if (latest.status !== "succeeded") {
        // Stream ended early (e.g. server restart); leave the job in the list.
        return;
      }
      const list = await loadAssets();
      const rendered: GeneratedImage[] = [];
      for (const a of list.filter((a) => a.job_id === latest.id)) {
        const url = await fetchAssetUrl(a.id);
        if (!url) continue;
        rendered.push({ src: url, seed: a.seed, width: a.width, height: a.height, steps: a.steps });
      }
      setImages(rendered);
    } catch (err) {
      if (controller.signal.aborted) return;
      setError(err instanceof Error ? err.message : "Generation failed");
    } finally {
      setLoading(false);
      abortRef.current = null;
    }
  }

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Images</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Generate images with SDXL. On an RTX 3060 12GB a 1024px image takes roughly 15–40 seconds.
      </p>

      <div className="grid gap-6 lg:grid-cols-[320px_1fr]">
        <form onSubmit={generate} className="space-y-4 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div>
            <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-model">Model</label>
            <input
              id="img-model"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
            />
          </div>

          <div>
            <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-prompt">Prompt</label>
            <textarea
              id="img-prompt"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              rows={4}
              placeholder="A photorealistic portrait of a person in soft studio light…"
              className="w-full resize-y rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
            />
          </div>

          <div>
            <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-negative">Negative prompt</label>
            <textarea
              id="img-negative"
              value={negative}
              onChange={(e) => setNegative(e.target.value)}
              rows={2}
              placeholder="blurry, low quality, extra fingers…"
              className="w-full resize-y rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-size">Size</label>
              <select
                id="img-size"
                value={size}
                onChange={(e) => setSize(e.target.value)}
                className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
              >
                {SIZES.map((s) => (
                  <option key={s} value={s}>{s}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-steps">Steps</label>
              <input
                id="img-steps"
                type="number"
                min={1}
                max={50}
                value={steps}
                onChange={(e) => setSteps(Number(e.target.value))}
                className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
              />
            </div>
            <div>
              <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-guidance">Guidance</label>
              <input
                id="img-guidance"
                type="number"
                min={0}
                max={20}
                step={0.5}
                value={guidance}
                onChange={(e) => setGuidance(Number(e.target.value))}
                className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
              />
            </div>
            <div>
              <label className="mb-1 block text-[12px] text-[var(--text2)]" htmlFor="img-seed">Seed</label>
              <input
                id="img-seed"
                value={seed}
                onChange={(e) => setSeed(e.target.value.replace(/[^0-9]/g, ""))}
                placeholder="random"
                className="w-full rounded border border-[var(--border)] bg-[var(--bg2)] px-2 py-1.5 text-[13px]"
              />
            </div>
          </div>

          <button
            type="submit"
            disabled={loading || !prompt.trim()}
            className="w-full rounded-lg bg-[var(--accent-strong)] px-3 py-2 text-[13px] font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            {loading ? "Generating…" : "Generate"}
          </button>
          {loading && (
            <div className="space-y-1">
              <p className="text-[12px] text-[var(--text2)]">
                {job
                  ? job.status === "queued"
                    ? "Queued — waiting for the worker…"
                    : `Generating ${job.progress_step}/${job.steps} steps…`
                  : "Submitting…"}
              </p>
              {job?.status === "running" && (
                <progress
                  className="h-1.5 w-full"
                  value={job.progress_step}
                  max={job.steps || 1}
                />
              )}
            </div>
          )}
          {error && <p className="text-[12px] text-[var(--err)]">{error}</p>}
        </form>

        <div className="space-y-6">
          <section>
            <div className="mb-2 text-[13px] font-medium">Result</div>
            {images.length === 0 ? (
              <div className="flex h-56 items-center justify-center rounded-lg border border-dashed border-[var(--border)] text-[13px] text-[var(--text2)]">
                {loading ? "Generating…" : "Your generated image will appear here."}
              </div>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                {images.map((img, i) => (
                  <figure key={i} className="overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--surface)]">
                    {/* eslint-disable-next-line @next/next/no-img-element -- data: URL, not optimizable */}
                    <img src={img.src} alt={prompt} className="w-full" />
                    <figcaption className="flex items-center justify-between gap-2 px-3 py-2 text-[11px] text-[var(--text2)]">
                      <span className="mono">{img.width}×{img.height} · {img.steps} steps · seed {img.seed}</span>
                      <a href={img.src} download={`image-${img.seed}.png`} className="text-[var(--link)] hover:underline">Download</a>
                    </figcaption>
                  </figure>
                ))}
              </div>
            )}
          </section>

          {assets.length > 0 && (
            <section>
              <div className="mb-2 text-[13px] font-medium">Recent</div>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
                {assets.map((a) =>
                  assetUrls[a.id] ? (
                    <a
                      key={a.id}
                      href={assetUrls[a.id]}
                      download={`image-${a.seed}.png`}
                      title={a.prompt}
                      className="group overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--surface)]"
                    >
                      {/* eslint-disable-next-line @next/next/no-img-element -- object URL from an authed fetch */}
                      <img src={assetUrls[a.id]} alt={a.prompt} className="aspect-square w-full object-cover group-hover:opacity-90" />
                    </a>
                  ) : (
                    <div key={a.id} className="aspect-square animate-pulse rounded-lg border border-[var(--border)] bg-[var(--surface2)]" />
                  ),
                )}
              </div>
            </section>
          )}
        </div>
      </div>
    </div>
  );
}
