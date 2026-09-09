"use client";

import { useCallback, useEffect, useState } from "react";
import DataTable, { Column, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { useAuth } from "@/context/AuthContext";
import { isPlatformAdmin } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import type { Tenant } from "@/lib/types";

const inputCls =
  "rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[13px] outline-none focus:border-[var(--accent)]";

export default function AdminPage() {
  const { claims } = useAuth();
  const [rows, setRows] = useState<Tenant[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await apiFetch<Tenant[]>("/api/v1/tenants"));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Không tải được tenants");
    }
  }, []);

  useEffect(() => {
    if (claims && isPlatformAdmin(claims.role)) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
      load();
    }
  }, [claims, load]);

  if (!claims || !isPlatformAdmin(claims.role)) {
    return (
      <div className="flex h-full items-center justify-center text-[13px] text-[var(--text2)]">
        🛡️ Trang này chỉ dành cho Platform Admin.
      </div>
    );
  }

  async function createTenant(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<Tenant>("/api/v1/tenants", {
        method: "POST",
        body: { name: name.trim() },
      });
      setNotice(`Tenant "${created.name}" tạo thành công (id: ${created.id}).`);
      setName("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Tạo tenant thất bại");
    } finally {
      setBusy(false);
    }
  }

  const columns: Column<Tenant>[] = [
    { key: "name", label: "Tên", render: (t) => <span className="font-medium">{t.name}</span> },
    { key: "status", label: "Trạng thái", render: (t) => <StatusBadge status={t.status} /> },
    { key: "id", label: "ID", render: (t) => <code className="text-[12px] text-[var(--link)]">{t.id}</code> },
    { key: "created_at", label: "Tạo lúc", render: (t) => <Time iso={t.created_at} /> },
    { key: "updated_at", label: "Cập nhật", render: (t) => <Time iso={t.updated_at} /> },
  ];

  return (
    <div className="mx-auto max-w-4xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Admin · Tenants</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">Quản lý tenant cấp platform (chỉ Platform Admin).</p>

      <form onSubmit={createTenant} className="mb-6 flex items-center gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Tên tenant (vd: acme)"
          className={`${inputCls} flex-1`}
        />
        <button
          type="submit"
          disabled={busy || !name.trim()}
          className="rounded-md bg-[var(--accent)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
        >
          {busy ? "Đang tạo…" : "+ Tạo tenant"}
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

      <DataTable columns={columns} rows={rows} empty="Chưa có tenant nào." />
    </div>
  );
}
