"use client";

import { useCallback, useEffect, useState } from "react";
import DataTable, { Column, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { useAuth } from "@/context/AuthContext";
import { isPlatformAdmin } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import type { Tenant, User } from "@/lib/types";

const inputCls =
  "rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[13px] focus:border-[var(--accent)]";

const ROLES = ["TENANT_ADMIN", "TENANT_DEVELOPER", "TENANT_VIEWER"] as const;

export default function AdminPage() {
  const { claims } = useAuth();
  const [rows, setRows] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  // --- users ---
  const [tenantId, setTenantId] = useState("");
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<string>("TENANT_ADMIN");
  const [userBusy, setUserBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setRows((await apiFetch<Tenant[]>("/api/v1/tenants")) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load tenants");
    } finally {
      setLoading(false);
    }
  }, []);

  const loadUsers = useCallback(async (id: string) => {
    if (!id) {
      setUsers([]);
      return;
    }
    setUsersLoading(true);
    try {
      setUsers((await apiFetch<User[]>(`/api/v1/users?tenant_id=${encodeURIComponent(id)}`)) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load users");
    } finally {
      setUsersLoading(false);
    }
  }, []);

  useEffect(() => {
    if (claims && isPlatformAdmin(claims.role)) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
      load();
    }
  }, [claims, load]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on tenant change
    loadUsers(tenantId);
  }, [tenantId, loadUsers]);

  if (!claims || !isPlatformAdmin(claims.role)) {
    return (
      <div className="flex h-full items-center justify-center text-[13px] text-[var(--text2)]">
        This page is for Platform Admins only.
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
      setNotice(`Tenant "${created.name}" created (id: ${created.id}).`);
      setName("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create tenant");
    } finally {
      setBusy(false);
    }
  }

  async function createUser(e: React.FormEvent) {
    e.preventDefault();
    if (!tenantId || !username.trim() || !password) return;
    setUserBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<User>("/api/v1/users", {
        method: "POST",
        body: { tenant_id: tenantId, username: username.trim(), email: email.trim(), password, role },
      });
      setNotice(`User "${created.username}" created with role ${created.role}.`);
      setUsername("");
      setEmail("");
      setPassword("");
      loadUsers(tenantId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create user");
    } finally {
      setUserBusy(false);
    }
  }

  const tenantColumns: Column<Tenant>[] = [
    { key: "name", label: "Name", render: (t) => <span className="font-medium">{t.name}</span> },
    { key: "status", label: "Status", render: (t) => <StatusBadge status={t.status} /> },
    { key: "id", label: "ID", render: (t) => <code className="text-[12px] text-[var(--link)]">{t.id}</code> },
    { key: "created_at", label: "Created", render: (t) => <Time iso={t.created_at} /> },
    { key: "updated_at", label: "Updated", render: (t) => <Time iso={t.updated_at} /> },
  ];

  const userColumns: Column<User>[] = [
    { key: "username", label: "Username", render: (u) => <span className="font-medium">{u.username}</span> },
    { key: "email", label: "Email", render: (u) => u.email || <span className="text-[var(--text2)]">—</span> },
    { key: "role", label: "Role" },
    { key: "status", label: "Status", render: (u) => <StatusBadge status={u.status} /> },
    { key: "id", label: "ID", render: (u) => <code className="text-[12px] text-[var(--link)]">{u.id.slice(0, 8)}…</code> },
  ];

  return (
    <div className="mx-auto max-w-4xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Admin · Tenants &amp; Users</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">Manage platform-level tenants and users (Platform Admin only).</p>

      <form onSubmit={createTenant} className="mb-6 flex items-center gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Tenant name (e.g. acme)"
          className={`${inputCls} flex-1`}
        />
        <button
          type="submit"
          disabled={busy || !name.trim()}
          className="rounded-md bg-[var(--accent-strong)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
        >
          {busy ? "Creating…" : "+ Create tenant"}
        </button>
      </form>

      {notice && (
        <div role="status" className="mb-4 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-[13px] text-[var(--ok)]">
          {notice}
        </div>
      )}
      {error && (
        <div role="alert" className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
          {error}
        </div>
      )}

      <DataTable columns={tenantColumns} rows={rows} loading={loading} empty="No tenants yet." />

      <h2 className="mb-1 mt-8 text-[15px] font-semibold">Users</h2>
      <p className="mb-4 text-[13px] text-[var(--text2)]">
        A tenant&apos;s first user should be <code className="text-[var(--link)]">TENANT_ADMIN</code> so they can sign in and manage that tenant.
      </p>

      <form onSubmit={createUser} className="mb-6 grid grid-cols-1 gap-3 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 md:grid-cols-2">
        <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
          <span>Tenant</span>
          <select value={tenantId} onChange={(e) => setTenantId(e.target.value)} className={inputCls} required>
            <option value="" disabled>
              — select a tenant —
            </option>
            {rows.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
          <span>Role</span>
          <select value={role} onChange={(e) => setRole(e.target.value)} className={inputCls}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
          <span>Username</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="alice" className={inputCls} required />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
          <span>Email</span>
          <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="alice@example.com" className={inputCls} />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            className={inputCls}
            required
          />
        </label>
        <div className="flex items-end md:col-span-2">
          <button
            type="submit"
            disabled={userBusy || !tenantId || !username.trim() || !password}
            className="rounded-md bg-[var(--accent-strong)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
          >
            {userBusy ? "Creating…" : "+ Create user"}
          </button>
        </div>
      </form>

      {tenantId ? (
        <DataTable columns={userColumns} rows={users} loading={usersLoading} empty="This tenant has no users yet." />
      ) : (
        <p className="text-[13px] text-[var(--text2)]">Select a tenant to view its users.</p>
      )}
    </div>
  );
}
