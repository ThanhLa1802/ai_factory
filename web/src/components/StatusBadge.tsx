"use client";

const COLOR: Record<string, string> = {
  READY: "text-[var(--ok)] border-[var(--ok)]/40 bg-[var(--ok)]/10",
  PENDING: "text-[var(--warn)] border-[var(--warn)]/40 bg-[var(--warn)]/10",
  PROVISIONING: "text-[var(--warn)] border-[var(--warn)]/40 bg-[var(--warn)]/10",
  STARTING: "text-[var(--warn)] border-[var(--warn)]/40 bg-[var(--warn)]/10",
  STOPPING: "text-[var(--warn)] border-[var(--warn)]/40 bg-[var(--warn)]/10",
  STOPPED: "text-[var(--text2)] border-[var(--border)] bg-[var(--surface2)]",
  FAILED: "text-[var(--err)] border-[var(--err)]/40 bg-[var(--err)]/10",
  DEGRADED: "text-[var(--err)] border-[var(--err)]/40 bg-[var(--err)]/10",
  ACTIVE: "text-[var(--ok)] border-[var(--ok)]/40 bg-[var(--ok)]/10",
  INACTIVE: "text-[var(--text2)] border-[var(--border)] bg-[var(--surface2)]",
  DISABLED: "text-[var(--text2)] border-[var(--border)] bg-[var(--surface2)]",
  EXPIRED: "text-[var(--err)] border-[var(--err)]/40 bg-[var(--err)]/10",
};

export default function StatusBadge({ status }: { status: string }) {
  const cls = COLOR[status.toUpperCase()] || "text-[var(--text2)] border-[var(--border)] bg-[var(--surface2)]";
  return (
    <span className={`inline-block rounded-full border px-2 py-0.5 text-[11px] font-medium ${cls}`}>
      {status}
    </span>
  );
}
