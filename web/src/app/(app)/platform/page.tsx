"use client";

import { useCallback, useEffect, useState } from "react";
import ApiKeysTab from "@/components/ApiKeysTab";
import DataTable, { Column } from "@/components/DataTable";
import { apiFetch } from "@/lib/api";
import type { UsageByModel, UsageResponse } from "@/lib/types";

type Tab = "usage" | "keys";

const TABS: { id: Tab; label: string }[] = [
  { id: "usage", label: "Usage" },
  { id: "keys", label: "API Keys" },
];

function fmt(n: number): string {
  return n.toLocaleString("vi-VN");
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
      <div className="text-[12px] text-[var(--text2)]">{label}</div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
    </div>
  );
}

function UsageTab() {
  const [data, setData] = useState<UsageResponse | null>(null);
  const [days, setDays] = useState(30);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await apiFetch<UsageResponse>(`/api/v1/usage?days=${days}`));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Không tải được usage");
    }
  }, [days]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on days
    load();
  }, [load]);

  if (!data) {
    return <div className="py-10 text-center text-[13px] text-[var(--text2)]">{error || "Đang tải…"}</div>;
  }

  const maxTokens = Math.max(1, ...data.daily.map((d) => d.prompt_tokens + d.completion_tokens));

  const modelColumns: Column<UsageByModel>[] = [
    { key: "model", label: "Model", render: (m) => <code className="text-[12px] text-[var(--link)]">{m.model}</code> },
    { key: "requests", label: "Request", render: (m) => fmt(m.requests) },
    { key: "prompt_tokens", label: "Prompt tokens", render: (m) => fmt(m.prompt_tokens) },
    { key: "completion_tokens", label: "Completion tokens", render: (m) => fmt(m.completion_tokens) },
    { key: "total_tokens", label: "Tổng tokens", render: (m) => <span className="font-medium">{fmt(m.total_tokens)}</span> },
  ];

  return (
    <div>
      <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatCard label="Tokens hôm nay" value={fmt(data.today.total_tokens)} />
        <StatCard label="Requests hôm nay" value={fmt(data.today.requests)} />
        <StatCard label="Tokens 30 ngày" value={fmt(data.month.total_tokens)} />
        <StatCard label="Requests 30 ngày" value={fmt(data.month.requests)} />
      </div>

      <div className="mb-6 grid grid-cols-2 gap-3">
        <StatCard label="Prompt tokens (30 ngày)" value={fmt(data.month.prompt_tokens)} />
        <StatCard label="Completion tokens (30 ngày)" value={fmt(data.month.completion_tokens)} />
      </div>

      <div className="mb-2 flex items-center gap-2">
        <span className="text-[13px] text-[var(--text2)]">Biểu đồ token theo ngày</span>
        <div className="ml-auto flex gap-1">
          {[7, 30].map((n) => (
            <button
              key={n}
              onClick={() => setDays(n)}
              className={`rounded px-2 py-1 text-[12px] ${
                days === n ? "bg-[var(--surface2)] text-white" : "text-[var(--text2)] hover:text-white"
              }`}
            >
              {n} ngày
            </button>
          ))}
        </div>
      </div>
      <div className="mb-6 flex h-40 items-end gap-1 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-3">
        {data.daily.length === 0 && (
          <div className="w-full text-center text-[12px] text-[var(--text2)]">Chưa có usage trong khoảng này.</div>
        )}
        {data.daily.map((d) => {
          const total = d.prompt_tokens + d.completion_tokens;
          return (
            <div
              key={d.date}
              title={`${d.date}: ${fmt(total)} tokens`}
              className="min-w-[6px] flex-1 rounded-t bg-[var(--accent)]"
              style={{ height: `${Math.max(2, (total / maxTokens) * 100)}%` }}
            />
          );
        })}
      </div>

      <div className="mb-3 text-[13px] font-medium">Theo model (30 ngày)</div>
      <DataTable columns={modelColumns} rows={data.by_model} empty="Chưa có usage theo model." />
    </div>
  );
}

export default function PlatformPage() {
  const [tab, setTab] = useState<Tab>("usage");

  return (
    <div className="mx-auto max-w-5xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Platform</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Thống kê usage và quản lý API key cho tài khoản của bạn.
      </p>

      <div className="mb-6 flex gap-1 border-b border-[var(--border)]">
        {TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`rounded-t-md px-4 py-2 text-[13px] ${
              tab === t.id ? "border-b-2 border-[var(--accent)] text-white" : "text-[var(--text2)] hover:text-white"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "usage" && <UsageTab />}
      {tab === "keys" && <ApiKeysTab />}
    </div>
  );
}
