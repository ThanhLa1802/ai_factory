"use client";

import { useCallback, useEffect, useState } from "react";
import DataTable, { Column, ShortId, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import { useAuth } from "@/context/AuthContext";
import { isPlatformAdmin } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import type { BillingWallet, LedgerEntry, ModelPrice, TopupTransaction } from "@/lib/types";

// Money is integer micro-credits: 1 credit = 1 currency unit = 1,000,000 µcr.
const MICRO = 1_000_000;

const inputCls =
  "rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[13px] focus:border-[var(--accent)]";
const btnCls =
  "rounded-md bg-[var(--accent-strong)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40";

// fmtMicro renders micro-credits as a human amount (1 credit = 1 currency unit).
// maxFractionDigits defaults to 6 (ledger charges can be sub-cent); balances
// pass 2 for a tidy money display.
function fmtMicro(micro: number, currency = "USD", maxFractionDigits = 6): string {
  const num = (Math.abs(micro) / MICRO).toLocaleString("en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: maxFractionDigits,
  });
  const prefix = currency === "USD" ? "$" : "";
  const suffix = currency === "USD" ? "" : ` ${currency}`;
  return `${micro < 0 ? "-" : ""}${prefix}${num}${suffix}`;
}

// creditsInputToMicro parses a credits/currency amount typed by the user into
// micro-credits; returns null when it is not a finite number.
function creditsInputToMicro(s: string): number | null {
  const n = Number(s.trim());
  if (!Number.isFinite(n)) return null;
  return Math.round(n * MICRO);
}

function microToCreditsInput(micro: number): string {
  return String(micro / MICRO);
}

function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function ErrorBanner({ msg }: { msg: string | null }) {
  if (!msg) return null;
  return (
    <div role="alert" className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
      {msg}
    </div>
  );
}

function Notice({ msg }: { msg: string | null }) {
  if (!msg) return null;
  return (
    <div role="status" className="mb-4 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-[13px] text-[var(--ok)]">
      {msg}
    </div>
  );
}

function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
      <div className="text-[12px] text-[var(--text2)]">{label}</div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
      {hint && <div className="mt-0.5 text-[11px] text-[var(--text2)]">{hint}</div>}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-[12px] text-[var(--text2)]">
      <span>{label}</span>
      {children}
    </label>
  );
}

export default function BillingTab() {
  const { claims } = useAuth();
  // Top-up is granted to tenant + platform admins (billing.manage); editing the
  // per-model token price is platform-admin only (billing.pricing). Everyone
  // with billing.read can still see the wallet, ledger and price list.
  const canTopUp = claims?.role === "PLATFORM_ADMIN" || claims?.role === "TENANT_ADMIN";
  const canEditPricing = isPlatformAdmin(claims?.role);

  const [wallet, setWallet] = useState<BillingWallet | null>(null);
  const [ledger, setLedger] = useState<LedgerEntry[]>([]);
  const [prices, setPrices] = useState<ModelPrice[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [topUpAmount, setTopUpAmount] = useState("");
  const [topUpBusy, setTopUpBusy] = useState(false);

  const [model, setModel] = useState("");
  const [priceInput, setPriceInput] = useState("");
  const [priceOutput, setPriceOutput] = useState("");
  const [editModel, setEditModel] = useState<string | null>(null);
  const [priceBusy, setPriceBusy] = useState(false);

  const currency = wallet?.currency || "USD";

  const load = useCallback(async () => {
    try {
      const [w, l, p] = await Promise.all([
        apiFetch<BillingWallet>("/api/v1/billing/wallet"),
        apiFetch<LedgerEntry[]>("/api/v1/billing/ledger?limit=50"),
        apiFetch<ModelPrice[]>("/api/v1/billing/pricing"),
      ]);
      setWallet(w);
      setLedger(l ?? []);
      setPrices(p ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load billing");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function topUp(e: React.FormEvent) {
    e.preventDefault();
    const amount = creditsInputToMicro(topUpAmount);
    if (amount === null || amount <= 0) return;
    setTopUpBusy(true);
    setError(null);
    setNotice(null);
    try {
      const tx = await apiFetch<TopupTransaction>("/api/v1/billing/topup", {
        method: "POST",
        headers: { "Idempotency-Key": newIdempotencyKey() },
        body: { amount, currency },
      });
      setNotice(`Topped up ${fmtMicro(tx.amount, tx.currency)} — wallet credited.`);
      setTopUpAmount("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to top up");
    } finally {
      setTopUpBusy(false);
    }
  }

  function editPrice(p: ModelPrice) {
    setModel(p.model);
    setPriceInput(microToCreditsInput(p.price_per_million_input_tokens));
    setPriceOutput(microToCreditsInput(p.price_per_million_output_tokens));
    setEditModel(p.model);
  }

  function resetPrice() {
    setModel("");
    setPriceInput("");
    setPriceOutput("");
    setEditModel(null);
  }

  async function savePrice(e: React.FormEvent) {
    e.preventDefault();
    if (!model.trim()) return;
    const inMicro = creditsInputToMicro(priceInput);
    const outMicro = creditsInputToMicro(priceOutput);
    if (inMicro === null || outMicro === null || inMicro < 0 || outMicro < 0) return;
    setPriceBusy(true);
    setError(null);
    setNotice(null);
    try {
      const saved = await apiFetch<ModelPrice>("/api/v1/billing/pricing", {
        method: "PUT",
        body: {
          model: model.trim(),
          currency,
          price_per_million_input_tokens: inMicro,
          price_per_million_output_tokens: outMicro,
        },
      });
      setNotice(`Price for "${saved.model}" saved.`);
      resetPrice();
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save price");
    } finally {
      setPriceBusy(false);
    }
  }

  const ledgerColumns: Column<LedgerEntry>[] = [
    { key: "created_at", label: "When", render: (l) => <Time iso={l.created_at} /> },
    { key: "kind", label: "Kind", render: (l) => <StatusBadge status={l.kind} /> },
    {
      key: "amount",
      label: "Amount",
      render: (l) => (
        <span className={`mono font-medium ${l.amount >= 0 ? "text-[var(--ok)]" : "text-[var(--err)]"}`}>
          {l.amount >= 0 ? "+" : ""}
          {fmtMicro(l.amount, l.currency)}
        </span>
      ),
    },
    { key: "balance_after", label: "Balance after", render: (l) => <span className="mono">{fmtMicro(l.balance_after, l.currency)}</span> },
    {
      key: "ref_id",
      label: "Reference",
      render: (l) =>
        l.ref_id ? (
          <span className="inline-flex items-center gap-1.5">
            <span className="text-[12px] text-[var(--text2)]">{l.ref_type}</span>
            <ShortId id={l.ref_id} />
          </span>
        ) : (
          <span className="text-[var(--text2)]">—</span>
        ),
    },
  ];

  const priceColumns: Column<ModelPrice>[] = [
    { key: "model", label: "Model", render: (p) => <code className="text-[12px] text-[var(--link)]">{p.model}</code> },
    { key: "input", label: `Input / 1M`, render: (p) => <span className="mono">{fmtMicro(p.price_per_million_input_tokens, p.currency)}</span> },
    { key: "output", label: `Output / 1M`, render: (p) => <span className="mono">{fmtMicro(p.price_per_million_output_tokens, p.currency)}</span> },
    { key: "currency", label: "Currency" },
  ];

  if (loading) {
    return <div className="py-10 text-center text-[13px] text-[var(--text2)]">Loading…</div>;
  }

  return (
    <div>
      <ErrorBanner msg={error} />
      <Notice msg={notice} />

      <div className="mb-6 grid grid-cols-1 gap-3 md:grid-cols-3">
        <StatCard label="Available" value={wallet ? fmtMicro(wallet.available, currency, 2) : "—"} hint="balance − reserved" />
        <StatCard label="Balance" value={wallet ? fmtMicro(wallet.balance, currency, 2) : "—"} hint={currency} />
        <StatCard label="Reserved (holds)" value={wallet ? fmtMicro(wallet.reserved, currency, 2) : "—"} hint="active inference holds" />
      </div>

      {canTopUp ? (
        <div className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">Top up wallet</div>
          <form onSubmit={topUp} className="flex flex-wrap items-end gap-3">
            <Field label={`Amount (${currency})`}>
              <input
                type="number"
                min={0}
                step="0.01"
                required
                value={topUpAmount}
                onChange={(e) => setTopUpAmount(e.target.value)}
                placeholder="10.00"
                className={inputCls}
              />
            </Field>
            <button type="submit" disabled={topUpBusy || !topUpAmount.trim()} className={btnCls}>
              {topUpBusy ? "Charging…" : "+ Top up"}
            </button>
          </form>
          <p className="mt-2 text-[11px] text-[var(--text2)]">
            Charged through the configured payment provider (mock in dev); idempotent per submission.
          </p>
        </div>
      ) : (
        <p className="mb-6 text-[12px] text-[var(--text2)]">
          Only tenant/platform admins can top up the wallet.
        </p>
      )}

      <div className="mb-3 text-[13px] font-medium">Recent ledger</div>
      <div className="mb-6">
        <DataTable columns={ledgerColumns} rows={ledger} empty="No ledger entries yet." />
      </div>

      <div className="mb-3 text-[13px] font-medium">Model pricing</div>
      {canEditPricing && (
        <div className="mb-4 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">{editModel ? `Edit price — ${editModel}` : "Set model price"}</div>
          <form onSubmit={savePrice} className="grid grid-cols-1 gap-3 md:grid-cols-4">
            <Field label="Model">
              <input
                required
                value={model}
                onChange={(e) => setModel(e.target.value)}
                disabled={!!editModel}
                placeholder="qwen-3b"
                className={`${inputCls} disabled:opacity-50`}
              />
            </Field>
            <Field label={`Input (${currency} / 1M tokens)`}>
              <input
                type="number"
                min={0}
                step="0.000001"
                required
                value={priceInput}
                onChange={(e) => setPriceInput(e.target.value)}
                placeholder="0.15"
                className={inputCls}
              />
            </Field>
            <Field label={`Output (${currency} / 1M tokens)`}>
              <input
                type="number"
                min={0}
                step="0.000001"
                required
                value={priceOutput}
                onChange={(e) => setPriceOutput(e.target.value)}
                placeholder="0.60"
                className={inputCls}
              />
            </Field>
            <div className="flex items-end gap-2">
              <button type="submit" disabled={priceBusy || !model.trim()} className={btnCls}>
                {priceBusy ? "Saving…" : editModel ? "Update" : "Save"}
              </button>
              {editModel && (
                <button
                  type="button"
                  onClick={resetPrice}
                  className="rounded-md border border-[var(--border)] px-3 py-2 text-[13px] text-[var(--text2)] hover:bg-[var(--surface2)] hover:text-[var(--text)]"
                >
                  Cancel
                </button>
              )}
            </div>
          </form>
        </div>
      )}
      <DataTable
        columns={priceColumns}
        rows={prices}
        empty="No model prices configured yet."
        actions={
          canEditPricing
            ? (p) => (
                <button onClick={() => editPrice(p)} className="text-[12px] text-[var(--link)] hover:underline">
                  Edit
                </button>
              )
            : undefined
        }
      />
    </div>
  );
}
