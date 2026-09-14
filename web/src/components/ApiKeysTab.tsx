"use client";

import { useCallback, useEffect, useState } from "react";
import ConfirmDialog from "@/components/ConfirmDialog";
import CopyButton from "@/components/CopyButton";
import DataTable, { Column, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { ApiError, apiFetch } from "@/lib/api";
import type { APIKey } from "@/lib/types";

export default function ApiKeysTab() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 403 = the signed-in role lacks key.manage (only admins have it). Render an
  // explanation instead of a raw "permission denied" from the API.
  const [forbidden, setForbidden] = useState(false);
  // The full key is returned exactly once by the server; keep it only until the
  // user dismisses it. It is never persisted.
  const [createdKey, setCreatedKey] = useState<{ name: string; key: string } | null>(null);
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    try {
      setKeys((await apiFetch<APIKey[]>("/api/v1/api-keys")) ?? []);
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        setForbidden(true);
      } else {
        setError(err instanceof Error ? err.message : "Failed to load API keys");
      }
    } finally {
      setLoading(false);
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
    try {
      const created = await apiFetch<{ id: string; name: string; key: string }>("/api/v1/api-keys", {
        method: "POST",
        body: { name: name.trim() },
      });
      setCreatedKey({ name: created.name, key: created.key });
      setName("");
      load();
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        setForbidden(true);
      } else {
        setError(err instanceof Error ? err.message : "Failed to create key");
      }
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: string) {
    try {
      await apiFetch(`/api/v1/api-keys/${id}/revoke`, { method: "POST" });
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to revoke key");
    } finally {
      setConfirmId(null);
    }
  }

  const columns: Column<APIKey>[] = [
    { key: "name", label: "Name", render: (k) => <span className="font-medium">{k.name}</span> },
    { key: "id", label: "ID", render: (k) => <code className="text-[12px] text-[var(--link)]">{k.id.slice(0, 8)}…</code> },
    { key: "status", label: "Status", render: (k) => <StatusBadge status={k.status} /> },
    { key: "expires_at", label: "Expires", render: (k) => (k.expires_at ? <Time iso={k.expires_at} /> : <span className="text-[var(--text2)]">Never</span>) },
    { key: "created_at", label: "Created", render: (k) => <Time iso={k.created_at} /> },
  ];

  if (forbidden) {
    return (
      <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-5 text-[13px] leading-relaxed text-[var(--text2)]">
        You don&apos;t have permission to manage API keys. Ask a tenant or platform admin to create one for you.
      </div>
    );
  }

  return (
    <div>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Keys authenticate `/v1/chat/completions` (Authorization: Bearer sk-…).
      </p>

      {!loading && (
        <form onSubmit={createKey} className="mb-6 flex items-center gap-2">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Key name (e.g. prod-bot)"
            className="flex-1 rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[14px] focus:border-[var(--accent)]"
          />
          <button
            type="submit"
            disabled={busy || !name.trim()}
            className="rounded-md bg-[var(--accent-strong)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
          >
            {busy ? "Creating…" : "+ Create key"}
          </button>
        </form>
      )}

      {createdKey && (
        <div className="mb-6 rounded-lg border border-[var(--ok)]/40 bg-[var(--ok)]/10 p-4">
          <div className="mb-2 text-[13px] font-medium text-[var(--ok)]">
            Key &quot;{createdKey.name}&quot; is shown only once — copy it now.
          </div>
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 select-all break-all rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[12px]">
              {createdKey.key}
            </code>
            <CopyButton value={createdKey.key} />
            <button
              type="button"
              onClick={() => setCreatedKey(null)}
              className="shrink-0 rounded-md px-2 py-1 text-[12px] text-[var(--text2)] hover:text-[var(--text)]"
            >
              Close
            </button>
          </div>
        </div>
      )}
      {error && (
        <div role="alert" className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
          {error}
        </div>
      )}

      <DataTable
        columns={columns}
        rows={keys}
        loading={loading}
        empty="No API keys yet."
        actions={(k) =>
          k.status === "ACTIVE" ? (
            <button onClick={() => setConfirmId(k.id)} className="text-[12px] text-[var(--err)] hover:underline">
              Revoke
            </button>
          ) : (
            <span className="text-[12px] text-[var(--text2)]">—</span>
          )
        }
      />

      <ConfirmDialog
        open={!!confirmId}
        title="Revoke this API key?"
        message="The key stops working immediately and cannot be reactivated. It stays listed for audit."
        confirmLabel="Revoke"
        danger
        onConfirm={() => confirmId && revoke(confirmId)}
        onCancel={() => setConfirmId(null)}
      />
    </div>
  );
}
