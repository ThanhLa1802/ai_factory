"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/context/AuthContext";

export default function LoginForm() {
  const { login, token } = useAuth();
  const router = useRouter();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("admin1234");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Already signed in? Go straight to the app.
  useEffect(() => {
    if (token) router.replace("/chat");
  }, [token, router]);
  if (token) return null;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(username, password);
      router.push("/chat");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Đăng nhập thất bại");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="w-full max-w-sm rounded-xl border border-[var(--border)] bg-[var(--surface)] p-8 shadow-2xl">
      <div className="mb-6 text-center">
        <div className="text-2xl font-semibold">AI Factory</div>
        <div className="text-[13px] text-[var(--text2)]">Sign in to the inference platform</div>
      </div>

      <form onSubmit={onSubmit} className="flex flex-col gap-4">
        <label className="flex flex-col gap-1 text-[13px]">
          <span className="text-[var(--text2)]">Username</span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            className="rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[14px] outline-none focus:border-[var(--accent)]"
          />
        </label>

        <label className="flex flex-col gap-1 text-[13px]">
          <span className="text-[var(--text2)]">Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            className="rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[14px] outline-none focus:border-[var(--accent)]"
          />
        </label>

        {error && (
          <div className="rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
            {error}
          </div>
        )}

        <button
          type="submit"
          disabled={busy}
          className="rounded-md bg-[var(--accent)] px-4 py-2.5 text-[14px] font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>

      <p className="mt-6 text-center text-[12px] text-[var(--text2)]">
        Seed demo: <code className="text-[var(--link)]">admin</code> /{" "}
        <code className="text-[var(--link)]">admin1234</code>
      </p>
    </div>
  );
}
