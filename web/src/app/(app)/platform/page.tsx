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

// YYYY-MM-DD theo UTC, khớp với cách server nhóm ngày (created_at AT TIME ZONE 'UTC').
function utcDay(daysAgo: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - daysAgo);
  return d.toISOString().slice(0, 10);
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

  // Server chỉ trả về các ngày CÓ usage; điền các ngày trống (0 token) để biểu đồ
  // thể hiện đúng chuỗi ngày liên tục của cửa sổ đã chọn, thay vì dồn các ngày có
  // dữ liệu lại thành vài cột bự.
  const byDate = new Map(data.daily.map((d) => [d.date, d] as const));
  const series = Array.from({ length: days }, (_, i) => {
    const date = utcDay(days - 1 - i); // cũ → mới
    const p = byDate.get(date);
    return { date, total: p ? p.prompt_tokens + p.completion_tokens : 0 };
  });
  const maxTokens = Math.max(1, ...series.map((s) => s.total));
  // Cứ mỗi labelEvery cột thì hiển thị 1 nhãn ngày (MM-DD) để biểu đồ đọc được
  // mà không bị rối khi chọn 30 ngày.
  const labelEvery = days <= 14 ? 1 : Math.ceil(series.length / 8);

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
                days === n ? "bg-[var(--surface2)] text-[var(--text)]" : "text-[var(--text2)] hover:text-[var(--text)]"
              }`}
            >
              {n} ngày
            </button>
          ))}
        </div>
      </div>
      <div className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-3">
        <div className="flex h-40 items-end gap-1">
          {data.daily.length === 0 ? (
            <div className="w-full text-center text-[12px] text-[var(--text2)]">Chưa có usage trong khoảng này.</div>
          ) : (
            series.map((s) => (
              <div
                key={s.date}
                title={`${s.date}: ${fmt(s.total)} tokens`}
                className="min-w-[6px] flex-1 rounded-t bg-[var(--accent)]"
                style={{ height: `${Math.max(2, (s.total / maxTokens) * 100)}%` }}
              />
            ))
          )}
        </div>
        {data.daily.length > 0 && (
          <div className="mt-1 flex gap-1">
            {series.map((s, i) => (
              <div key={s.date} className="min-w-[6px] flex-1 text-center text-[10px] leading-none text-[var(--text2)]">
                {i % labelEvery === 0 ? s.date.slice(5) : ""}
              </div>
            ))}
          </div>
        )}
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

      <div role="tablist" aria-label="Platform sections" className="mb-6 flex gap-1 border-b border-[var(--border)]">
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`panel-${t.id}`}
            onClick={() => setTab(t.id)}
            className={`rounded-t-md px-4 py-2 text-[13px] ${
              tab === t.id ? "border-b-2 border-[var(--accent)] text-[var(--text)]" : "text-[var(--text2)] hover:text-[var(--text)]"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "usage" && (
        <div role="tabpanel" id="panel-usage" aria-labelledby="tab-usage">
          <UsageTab />
        </div>
      )}
      {tab === "keys" && (
        <div role="tabpanel" id="panel-keys" aria-labelledby="tab-keys">
          <ApiKeysTab />
        </div>
      )}
    </div>
  );
}
