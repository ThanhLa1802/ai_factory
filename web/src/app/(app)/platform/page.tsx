"use client";

import { useCallback, useEffect, useState } from "react";
import ApiKeysTab from "@/components/ApiKeysTab";
import BillingTab from "@/components/BillingTab";
import DataTable, { Column } from "@/components/DataTable";
import TabList from "@/components/TabList";
import { apiFetch } from "@/lib/api";
import type { UsageByModel, UsageResponse } from "@/lib/types";

type Tab = "usage" | "billing" | "keys";

const TABS: { id: Tab; label: string }[] = [
  { id: "usage", label: "Usage" },
  { id: "billing", label: "Billing" },
  { id: "keys", label: "API Keys" },
];

function fmt(n: number): string {
  return n.toLocaleString("vi-VN");
}

// YYYY-MM-DD in UTC, matching how the server groups days (created_at AT TIME ZONE 'UTC').
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
      setError(err instanceof Error ? err.message : "Failed to load usage");
    }
  }, [days]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on days
    load();
  }, [load]);

  if (!data) {
    return <div className="py-10 text-center text-[13px] text-[var(--text2)]">{error || "Loading…"}</div>;
  }

  // The server only returns days that HAVE usage; fill the empty days (0 tokens)
  // so the chart shows the selected window as a continuous day series instead of
  // packing the days with data into a few fat bars.
  const byDate = new Map(data.daily.map((d) => [d.date, d] as const));
  const series = Array.from({ length: days }, (_, i) => {
    const date = utcDay(days - 1 - i); // oldest → newest
    const p = byDate.get(date);
    return {
      date,
      prompt: p ? p.prompt_tokens : 0,
      completion: p ? p.completion_tokens : 0,
      total: p ? p.prompt_tokens + p.completion_tokens : 0,
      requests: p ? p.requests : 0,
    };
  });
  const maxTokens = Math.max(1, ...series.map((s) => s.total));
  const windowTotal = series.reduce((sum, s) => sum + s.total, 0);
  const peak = series.reduce((a, s) => (s.total > a.total ? s : a), series[0]);
  // Show one date label (MM-DD) every labelEvery bars so the chart stays readable
  // without crowding when 30 days are selected.
  const labelEvery = days <= 14 ? 1 : Math.ceil(series.length / 8);

  const modelColumns: Column<UsageByModel>[] = [
    { key: "model", label: "Model", render: (m) => <code className="text-[12px] text-[var(--link)]">{m.model}</code> },
    { key: "requests", label: "Request", render: (m) => fmt(m.requests) },
    { key: "prompt_tokens", label: "Prompt tokens", render: (m) => fmt(m.prompt_tokens) },
    { key: "completion_tokens", label: "Completion tokens", render: (m) => fmt(m.completion_tokens) },
    { key: "total_tokens", label: "Total tokens", render: (m) => <span className="font-medium">{fmt(m.total_tokens)}</span> },
  ];

  return (
    <div>
      <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatCard label="Tokens today" value={fmt(data.today.total_tokens)} />
        <StatCard label="Requests today" value={fmt(data.today.requests)} />
        <StatCard label="Tokens (30 days)" value={fmt(data.month.total_tokens)} />
        <StatCard label="Requests (30 days)" value={fmt(data.month.requests)} />
      </div>

      <div className="mb-6 grid grid-cols-2 gap-3">
        <StatCard label="Prompt tokens (30 days)" value={fmt(data.month.prompt_tokens)} />
        <StatCard label="Completion tokens (30 days)" value={fmt(data.month.completion_tokens)} />
      </div>

      <div className="mb-2 flex items-center gap-2">
        <span className="text-[13px] text-[var(--text2)]">Daily token usage</span>
        <div className="ml-auto flex gap-1">
          {[7, 30].map((n) => (
            <button
              key={n}
              onClick={() => setDays(n)}
              className={`rounded px-2 py-1 text-[12px] ${
                days === n ? "bg-[var(--surface2)] text-[var(--text)]" : "text-[var(--text2)] hover:text-[var(--text)]"
              }`}
            >
              {n} days
            </button>
          ))}
        </div>
      </div>
      <div className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-3">
        <div
          role="img"
          aria-label={`Daily token bar chart over the last ${days} days: ${fmt(windowTotal)} tokens total, peak on ${peak.date} with ${fmt(peak.total)} tokens.`}
        >
          <div aria-hidden="true" className="flex h-40 items-end gap-1">
            {data.daily.length === 0 ? (
              <div className="w-full text-center text-[12px] text-[var(--text2)]">No usage in this range.</div>
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
            <div aria-hidden="true" className="mt-1 flex gap-1">
              {series.map((s, i) => (
                <div key={s.date} className="min-w-[6px] flex-1 text-center text-[10px] leading-none text-[var(--text2)]">
                  {i % labelEvery === 0 ? s.date.slice(5) : ""}
                </div>
              ))}
            </div>
          )}
        </div>

        {data.daily.length > 0 && (
          <details className="mt-3">
            <summary className="cursor-pointer text-[12px] text-[var(--text2)] hover:text-[var(--text)]">
              View as table
            </summary>
            <div className="mt-2 max-h-64 overflow-y-auto">
              <table className="w-full text-left text-[12px]">
                <thead className="text-[var(--text2)]">
                  <tr>
                    <th className="py-1 font-medium">Date</th>
                    <th className="py-1 font-medium">Prompt</th>
                    <th className="py-1 font-medium">Completion</th>
                    <th className="py-1 font-medium">Total</th>
                    <th className="py-1 font-medium">Requests</th>
                  </tr>
                </thead>
                <tbody>
                  {series.map((s) => (
                    <tr key={s.date} className="border-t border-[var(--border)]">
                      <td className="mono py-1 text-[var(--text2)]">{s.date}</td>
                      <td className="py-1">{fmt(s.prompt)}</td>
                      <td className="py-1">{fmt(s.completion)}</td>
                      <td className="py-1 font-medium">{fmt(s.total)}</td>
                      <td className="py-1">{fmt(s.requests)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        )}
      </div>

      <div className="mb-3 text-[13px] font-medium">By model (30 days)</div>
      <DataTable columns={modelColumns} rows={data.by_model} empty="No usage by model yet." />
    </div>
  );
}

export default function PlatformPage() {
  const [tab, setTab] = useState<Tab>("usage");

  // Deep link: /platform?tab=keys|billing (and the /keys redirect) opens the tab.
  useEffect(() => {
    const t = new URLSearchParams(window.location.search).get("tab");
    // eslint-disable-next-line react-hooks/set-state-in-effect -- one-shot deep link
    if (t === "keys" || t === "billing") setTab(t);
  }, []);

  return (
    <div className="mx-auto max-w-5xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Platform</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Usage statistics, billing (wallet + pricing), and API key management for your account.
      </p>

      <TabList tabs={TABS} active={tab} onChange={(id) => setTab(id as Tab)} label="Platform sections" />

      {tab === "usage" && (
        <div role="tabpanel" tabIndex={0} id="panel-usage" aria-labelledby="tab-usage">
          <UsageTab />
        </div>
      )}
      {tab === "billing" && (
        <div role="tabpanel" tabIndex={0} id="panel-billing" aria-labelledby="tab-billing">
          <BillingTab />
        </div>
      )}
      {tab === "keys" && (
        <div role="tabpanel" tabIndex={0} id="panel-keys" aria-labelledby="tab-keys">
          <ApiKeysTab />
        </div>
      )}
    </div>
  );
}
