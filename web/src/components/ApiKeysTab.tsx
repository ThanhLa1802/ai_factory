"use client";

import { useCallback, useEffect, useState } from "react";
import DataTable, { Column, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { apiFetch } from "@/lib/api";
import type { APIKey } from "@/lib/types";

export default function ApiKeysTab() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setKeys(await apiFetch<APIKey[]>("/api/v1/api-keys"));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Không tải được danh sách key");
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function createKey(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<{ id: string; name: string; key: string }>("/api/v1/api-keys", {
        method: "POST",
        body: { name: name.trim() },
      });
      setNotice(`Đã tạo key "${created.name}". Chỉ hiển thị một lần: sk-…${created.key.slice(-8)}`);
      setName("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Tạo key thất bại");
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: string) {
    if (!window.confirm("Thu hồi API key này?")) return;
    try {
      await apiFetch(`/api/v1/api-keys/${id}`, { method: "DELETE" });
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Thu hồi thất bại");
    }
  }

  const columns: Column<APIKey>[] = [
    { key: "name", label: "Tên", render: (k) => <span className="font-medium">{k.name}</span> },
    { key: "id", label: "ID", render: (k) => <code className="text-[12px] text-[var(--link)]">{k.id.slice(0, 8)}…</code> },
    { key: "status", label: "Trạng thái", render: (k) => <StatusBadge status={k.status} /> },
    { key: "expires_at", label: "Hết hạn", render: (k) => (k.expires_at ? <Time iso={k.expires_at} /> : <span className="text-[var(--text2)]">Không</span>) },
    { key: "created_at", label: "Tạo lúc", render: (k) => <Time iso={k.created_at} /> },
  ];

  return (
    <div>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Key dùng cho `/v1/chat/completions` (Authorization: Bearer sk-…).
      </p>

      <form onSubmit={createKey} className="mb-6 flex items-center gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Tên key (vd: prod-bot)"
          className="flex-1 rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[14px] outline-none focus:border-[var(--accent)]"
        />
        <button
          type="submit"
          disabled={busy || !name.trim()}
          className="rounded-md bg-[var(--accent)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
        >
          {busy ? "Đang tạo…" : "+ Tạo key"}
        </button>
      </form>

      {notice && (
        <div className="mb-4 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-[13px] text-[var(--ok)]">
          {notice}
        </div>
      )}
      {error && (
        <div className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
          {error}
        </div>
      )}

      <DataTable
        columns={columns}
        rows={keys}
        empty="Chưa có API key nào."
        actions={(k) => (
          <button onClick={() => revoke(k.id)} className="text-[12px] text-[var(--err)] hover:underline">
            Thu hồi
          </button>
        )}
      />
    </div>
  );
}
