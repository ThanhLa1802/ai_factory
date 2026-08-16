"use client";

import type { ReactNode } from "react";

export interface Column<T> {
  key: string;
  label: string;
  render?: (row: T) => ReactNode;
  className?: string;
}

// DataTable is a minimal read-only table for control-plane lists.
export default function DataTable<T>({
  columns,
  rows,
  actions,
  empty = "Chưa có dữ liệu.",
}: {
  columns: Column<T>[];
  rows: T[];
  actions?: (row: T) => ReactNode;
  empty?: string;
}) {
  if (rows.length === 0) {
    return <div className="px-4 py-6 text-center text-[13px] text-[var(--text2)]">{empty}</div>;
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
      <table className="w-full text-left text-[13px]">
        <thead>
          <tr className="bg-[var(--surface2)] text-[var(--text2)]">
            {columns.map((c) => (
              <th key={c.key} className={`px-3 py-2.5 font-medium ${c.className || ""}`}>
                {c.label}
              </th>
            ))}
            {actions && <th className="px-3 py-2.5 font-medium">Hành động</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i} className="border-t border-[var(--border)] hover:bg-[var(--surface)]/60">
              {columns.map((c) => (
                <td key={c.key} className={`px-3 py-2.5 align-top ${c.className || ""}`}>
                  {c.render ? c.render(row) : String((row as Record<string, unknown>)[c.key] ?? "")}
                </td>
              ))}
              {actions && <td className="px-3 py-2.5 align-top">{actions(row)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function ShortId({ id }: { id: string }) {
  return (
    <code title={id} className="text-[12px] text-[var(--link)]">
      {id.slice(0, 8)}…
    </code>
  );
}

export function Time({ iso }: { iso: string }) {
  if (!iso) return <span className="text-[var(--text2)]">—</span>;
  const d = new Date(iso);
  return <span title={d.toLocaleString()}>{d.toLocaleString("vi-VN")}</span>;
}
