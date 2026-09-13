"use client";

import { useCallback, useEffect, useState } from "react";
import CopyButton from "@/components/CopyButton";
import DataTable, { Column, ShortId, Time } from "@/components/DataTable";
import StatusBadge from "@/components/StatusBadge";
import TabList from "@/components/TabList";
import { apiFetch } from "@/lib/api";
import type {
  Deployment,
  Model,
  ModelVersion,
  Quota,
  ServingTemplate,
  TemplateVersion,
} from "@/lib/types";

type Tab = "deployments" | "models" | "templates" | "quotas";

const TABS: { id: Tab; label: string }[] = [
  { id: "deployments", label: "Deployments" },
  { id: "models", label: "Models" },
  { id: "templates", label: "Serving Templates" },
  { id: "quotas", label: "Quotas" },
];

// A deployment in one of these states is no longer moving; anything else means
// the async worker is still transitioning it, so the list should refresh.
const TERMINAL = new Set(["READY", "STOPPED", "FAILED", "DEGRADED"]);

function Notice({ msg }: { msg: string | null }) {
  if (!msg) return null;
  return (
    <div role="status" className="mb-4 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-[13px] text-[var(--ok)]">
      {msg}
    </div>
  );
}

function ErrorBanner({ msg }: { msg: string | null }) {
  if (!msg) return null;
  return (
    <div role="alert" className="mb-4 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-[13px] text-[var(--err)]">
      {msg}
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

const inputCls =
  "rounded-md border border-[var(--border)] bg-[var(--bg2)] px-3 py-2 text-[13px] focus:border-[var(--accent)]";
const btnCls =
  "rounded-md bg-[var(--accent-strong)] px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40";

// IdCell renders a truncated id with a copy-to-clipboard affordance.
function IdCell({ id }: { id: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <ShortId id={id} />
      <CopyButton value={id} label="copy" />
    </span>
  );
}

export default function PlatformPage() {
  const [tab, setTab] = useState<Tab>("deployments");

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <h1 className="mb-1 text-lg font-semibold">Infrastructure</h1>
      <p className="mb-5 text-[13px] text-[var(--text2)]">
        Manage models, serving templates, deployments, and quotas for the tenant.
      </p>

      <TabList tabs={TABS} active={tab} onChange={(id) => setTab(id as Tab)} label="Infrastructure sections" />

      {tab === "deployments" && (
        <div role="tabpanel" tabIndex={0} id="panel-deployments" aria-labelledby="tab-deployments">
          <DeploymentsTab />
        </div>
      )}
      {tab === "models" && (
        <div role="tabpanel" tabIndex={0} id="panel-models" aria-labelledby="tab-models">
          <ModelsTab />
        </div>
      )}
      {tab === "templates" && (
        <div role="tabpanel" tabIndex={0} id="panel-templates" aria-labelledby="tab-templates">
          <TemplatesTab />
        </div>
      )}
      {tab === "quotas" && (
        <div role="tabpanel" tabIndex={0} id="panel-quotas" aria-labelledby="tab-quotas">
          <QuotasTab />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Deployments
// ---------------------------------------------------------------------------

function DeploymentsTab() {
  const [rows, setRows] = useState<Deployment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [region, setRegion] = useState("us-east-1");
  const [replicas, setReplicas] = useState(1);
  const [busy, setBusy] = useState(false);

  // Catalog selections replace the old free-text UUID inputs: pick a model →
  // its versions, pick a template → its versions.
  const [models, setModels] = useState<Model[]>([]);
  const [templates, setTemplates] = useState<ServingTemplate[]>([]);
  const [modelId, setModelId] = useState("");
  const [modelVersions, setModelVersions] = useState<ModelVersion[]>([]);
  const [modelVersionId, setModelVersionId] = useState("");
  const [templateId, setTemplateId] = useState("");
  const [templateVersions, setTemplateVersions] = useState<TemplateVersion[]>([]);
  const [templateVersionId, setTemplateVersionId] = useState("");

  const load = useCallback(async () => {
    try {
      setRows((await apiFetch<Deployment[]>("/api/v1/deployments")) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load deployments");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiFetch<Model[]>("/api/v1/models").catch(() => [] as Model[]),
      apiFetch<ServingTemplate[]>("/api/v1/templates").catch(() => [] as ServingTemplate[]),
    ]).then(([ms, ts]) => {
      if (cancelled) return;
      setModels(ms);
      setTemplates(ts);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  // Poll while any deployment row is still transitioning (PENDING→READY etc.).
  const hasPending = rows.some((r) => !TERMINAL.has(r.status));
  useEffect(() => {
    if (!hasPending) return;
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  }, [hasPending, load]);

  async function pickModel(id: string) {
    setModelId(id);
    setModelVersionId("");
    setModelVersions([]);
    if (!id) return;
    try {
      setModelVersions((await apiFetch<ModelVersion[]>(`/api/v1/models/${id}/versions`)) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load model versions");
    }
  }

  async function pickTemplate(id: string) {
    setTemplateId(id);
    setTemplateVersionId("");
    setTemplateVersions([]);
    if (!id) return;
    try {
      setTemplateVersions((await apiFetch<TemplateVersion[]>(`/api/v1/templates/${id}/versions`)) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load template versions");
    }
  }

  const canSubmit = !!name.trim() && !!modelVersionId && !!templateVersionId && !busy;

  async function createDeployment(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<Deployment>("/api/v1/deployments", {
        method: "POST",
        body: {
          name: name.trim(),
          region: region.trim(),
          desired_replicas: replicas,
          model_version_id: modelVersionId.trim(),
          template_version_id: templateVersionId.trim(),
        },
      });
      setNotice(`Deployment "${created.name}" created — status ${created.status} (the async worker moves it PENDING→READY).`);
      setName("");
      setModelId("");
      setModelVersions([]);
      setModelVersionId("");
      setTemplateId("");
      setTemplateVersions([]);
      setTemplateVersionId("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create deployment");
    } finally {
      setBusy(false);
    }
  }

  async function action(id: string, act: "start" | "stop") {
    setError(null);
    setNotice(null);
    try {
      await apiFetch<Deployment>(`/api/v1/deployments/${id}/${act}`, { method: "POST" });
      setNotice(`${act.toUpperCase()} requested (async).`);
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Action failed");
    }
  }

  const columns: Column<Deployment>[] = [
    { key: "name", label: "Name", render: (d) => <span className="font-medium">{d.name}</span> },
    { key: "status", label: "Status", render: (d) => <StatusBadge status={d.status} /> },
    { key: "region", label: "Region" },
    { key: "desired_replicas", label: "Replicas", render: (d) => String(d.desired_replicas) },
    {
      key: "model_version_id",
      label: "Model version",
      render: (d) => <IdCell id={d.model_version_id} />,
    },
    {
      key: "template_version_id",
      label: "Template version",
      render: (d) => <IdCell id={d.template_version_id} />,
    },
    { key: "workload_ref", label: "Workload", render: (d) => (d.workload_ref ? <ShortId id={d.workload_ref} /> : <span className="text-[var(--text2)]">—</span>) },
    { key: "updated_at", label: "Updated", render: (d) => <Time iso={d.updated_at} /> },
  ];

  return (
    <div>
      <div className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
        <div className="mb-3 text-[13px] font-medium">Create deployment</div>
        <form onSubmit={createDeployment} className="grid grid-cols-1 gap-3 md:grid-cols-5">
          <Field label="Name">
            <input required value={name} onChange={(e) => setName(e.target.value)} placeholder="chat-prod" className={inputCls} />
          </Field>
          <Field label="Region">
            <input required value={region} onChange={(e) => setRegion(e.target.value)} className={inputCls} />
          </Field>
          <Field label="Replicas">
            <input
              type="number"
              min={1}
              value={replicas}
              onChange={(e) => setReplicas(Number(e.target.value))}
              className={inputCls}
            />
          </Field>
          <Field label="Model">
            <select value={modelId} onChange={(e) => pickModel(e.target.value)} className={inputCls}>
              <option value="">— select a model —</option>
              {models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Model version">
            <select
              value={modelVersionId}
              onChange={(e) => setModelVersionId(e.target.value)}
              disabled={!modelId}
              className={inputCls}
            >
              <option value="">{modelId ? "— select a version —" : "select a model first"}</option>
              {modelVersions.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.version} · {v.id.slice(0, 8)}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Template">
            <select value={templateId} onChange={(e) => pickTemplate(e.target.value)} className={inputCls}>
              <option value="">— select a template —</option>
              {templates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Template version">
            <select
              value={templateVersionId}
              onChange={(e) => setTemplateVersionId(e.target.value)}
              disabled={!templateId}
              className={inputCls}
            >
              <option value="">{templateId ? "— select a version —" : "select a template first"}</option>
              {templateVersions.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.version} · {v.id.slice(0, 8)}
                </option>
              ))}
            </select>
          </Field>
          <div className="flex items-end md:col-span-5">
            <button type="submit" disabled={!canSubmit} className={btnCls}>
              {busy ? "Creating…" : "+ Create deployment"}
            </button>
          </div>
        </form>
        <p className="mt-2 text-[11px] text-[var(--text2)]">
          {models.length === 0 || templates.length === 0
            ? "No models/templates yet — create them under the Models / Serving Templates tabs first (each needs at least one version)."
            : "PENDING→READY is handled by the async worker (requires Kafka)."}
        </p>
      </div>

      <ErrorBanner msg={error} />
      <Notice msg={notice} />

      <DataTable
        columns={columns}
        rows={rows}
        loading={loading}
        empty="No deployments yet."
        actions={(d) => (
          <div className="flex gap-2">
            {d.status === "STOPPED" || d.status === "FAILED" ? (
              <button onClick={() => action(d.id, "start")} className="text-[12px] text-[var(--ok)] hover:underline">
                Start
              </button>
            ) : (
              <button onClick={() => action(d.id, "stop")} className="text-[12px] text-[var(--warn)] hover:underline">
                Stop
              </button>
            )}
          </div>
        )}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

function ModelsTab() {
  const [rows, setRows] = useState<Model[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [task, setTask] = useState("chat");
  const [framework, setFramework] = useState("transformers");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);

  const [modelId, setModelId] = useState("");
  const [version, setVersion] = useState("");
  const [artifactUri, setArtifactUri] = useState("");
  const [versionBusy, setVersionBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setRows((await apiFetch<Model[]>("/api/v1/models")) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load models");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function createModel(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<Model>("/api/v1/models", {
        method: "POST",
        body: { name: name.trim(), task, framework, description },
      });
      setNotice(`Model "${created.name}" created (id: ${created.id}).`);
      setName("");
      setDescription("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create model");
    } finally {
      setBusy(false);
    }
  }

  async function createVersion(e: React.FormEvent) {
    e.preventDefault();
    setVersionBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<ModelVersion>(`/api/v1/models/${modelId}/versions`, {
        method: "POST",
        body: { version: version.trim(), artifact_uri: artifactUri.trim() },
      });
      setNotice(`Version "${created.version}" created. Model version ID: ${created.id}`);
      setVersion("");
      setArtifactUri("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create version");
    } finally {
      setVersionBusy(false);
    }
  }

  const columns: Column<Model>[] = [
    { key: "name", label: "Name", render: (m) => <span className="font-medium">{m.name}</span> },
    { key: "task", label: "Task" },
    { key: "framework", label: "Framework" },
    { key: "status", label: "Status", render: (m) => <StatusBadge status={m.status} /> },
    { key: "id", label: "ID", render: (m) => <IdCell id={m.id} /> },
    { key: "created_at", label: "Created", render: (m) => <Time iso={m.created_at} /> },
  ];

  return (
    <div>
      <div className="mb-6 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">Create model</div>
          <form onSubmit={createModel} className="flex flex-col gap-3">
            <Field label="Name">
              <input required value={name} onChange={(e) => setName(e.target.value)} placeholder="qwen-3b" className={inputCls} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Task">
                <select value={task} onChange={(e) => setTask(e.target.value)} className={inputCls}>
                  <option value="chat">chat</option>
                  <option value="embedding">embedding</option>
                  <option value="image">image</option>
                </select>
              </Field>
              <Field label="Framework">
                <select value={framework} onChange={(e) => setFramework(e.target.value)} className={inputCls}>
                  <option value="transformers">transformers</option>
                  <option value="llama.cpp">llama.cpp</option>
                </select>
              </Field>
            </div>
            <Field label="Description">
              <input value={description} onChange={(e) => setDescription(e.target.value)} className={inputCls} />
            </Field>
            <button type="submit" disabled={busy} className={btnCls}>
              {busy ? "Creating…" : "+ Create model"}
            </button>
          </form>
        </div>

        <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">Add model version</div>
          <form onSubmit={createVersion} className="flex flex-col gap-3">
            <Field label="Model">
              <select value={modelId} onChange={(e) => setModelId(e.target.value)} className={inputCls} required>
                <option value="" disabled>
                  — select a model —
                </option>
                {rows.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Version">
              <input required value={version} onChange={(e) => setVersion(e.target.value)} placeholder="v1" className={inputCls} />
            </Field>
            <Field label="Artifact URI">
              <input value={artifactUri} onChange={(e) => setArtifactUri(e.target.value)} placeholder="s3://bucket/model" className={inputCls} />
            </Field>
            <button type="submit" disabled={versionBusy || !modelId} className={btnCls}>
              {versionBusy ? "Creating…" : "+ Create version"}
            </button>
          </form>
        </div>
      </div>

      <ErrorBanner msg={error} />
      <Notice msg={notice} />

      <DataTable columns={columns} rows={rows} loading={loading} empty="No models yet." />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Serving Templates
// ---------------------------------------------------------------------------

function TemplatesTab() {
  const [rows, setRows] = useState<ServingTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [runtime, setRuntime] = useState("python-worker");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);

  const [templateId, setTemplateId] = useState("");
  const [version, setVersion] = useState("");
  const [image, setImage] = useState("");
  const [versionBusy, setVersionBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setRows((await apiFetch<ServingTemplate[]>("/api/v1/templates")) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load templates");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function createTemplate(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<ServingTemplate>("/api/v1/templates", {
        method: "POST",
        body: { name: name.trim(), runtime, description },
      });
      setNotice(`Template "${created.name}" created (id: ${created.id}).`);
      setName("");
      setDescription("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create template");
    } finally {
      setBusy(false);
    }
  }

  async function createVersion(e: React.FormEvent) {
    e.preventDefault();
    setVersionBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await apiFetch<TemplateVersion>(`/api/v1/templates/${templateId}/versions`, {
        method: "POST",
        body: { version: version.trim(), image: image.trim() },
      });
      setNotice(`Template version "${created.version}" created. Template version ID: ${created.id}`);
      setVersion("");
      setImage("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create version");
    } finally {
      setVersionBusy(false);
    }
  }

  const columns: Column<ServingTemplate>[] = [
    { key: "name", label: "Name", render: (t) => <span className="font-medium">{t.name}</span> },
    { key: "runtime", label: "Runtime" },
    { key: "status", label: "Status", render: (t) => <StatusBadge status={t.status} /> },
    { key: "id", label: "ID", render: (t) => <IdCell id={t.id} /> },
    { key: "updated_at", label: "Updated", render: (t) => <Time iso={t.updated_at} /> },
  ];

  return (
    <div>
      <div className="mb-6 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">Create serving template</div>
          <form onSubmit={createTemplate} className="flex flex-col gap-3">
            <Field label="Name">
              <input required value={name} onChange={(e) => setName(e.target.value)} placeholder="cpu-1slot" className={inputCls} />
            </Field>
            <Field label="Runtime">
              <select value={runtime} onChange={(e) => setRuntime(e.target.value)} className={inputCls}>
                <option value="python-worker">python-worker</option>
                <option value="llama.cpp">llama.cpp</option>
              </select>
            </Field>
            <Field label="Description">
              <input value={description} onChange={(e) => setDescription(e.target.value)} className={inputCls} />
            </Field>
            <button type="submit" disabled={busy} className={btnCls}>
              {busy ? "Creating…" : "+ Create template"}
            </button>
          </form>
        </div>

        <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
          <div className="mb-3 text-[13px] font-medium">Add template version</div>
          <form onSubmit={createVersion} className="flex flex-col gap-3">
            <Field label="Template">
              <select value={templateId} onChange={(e) => setTemplateId(e.target.value)} className={inputCls} required>
                <option value="" disabled>
                  — select a template —
                </option>
                {rows.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Version">
              <input required value={version} onChange={(e) => setVersion(e.target.value)} placeholder="v1" className={inputCls} />
            </Field>
            <Field label="Image">
              <input value={image} onChange={(e) => setImage(e.target.value)} placeholder="ai-factory/worker:1.0" className={inputCls} />
            </Field>
            <button type="submit" disabled={versionBusy || !templateId} className={btnCls}>
              {versionBusy ? "Creating…" : "+ Create version"}
            </button>
          </form>
        </div>
      </div>

      <ErrorBanner msg={error} />
      <Notice msg={notice} />

      <DataTable columns={columns} rows={rows} loading={loading} empty="No serving templates yet." />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Quotas
// ---------------------------------------------------------------------------

function QuotasTab() {
  const [rows, setRows] = useState<Quota[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [quotaType, setQuotaType] = useState("tokens_per_day");
  const [limitValue, setLimitValue] = useState(1000);
  const [period, setPeriod] = useState("daily");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setRows((await apiFetch<Quota[]>("/api/v1/quotas")) ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load quotas");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount
    load();
  }, [load]);

  async function upsert(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await apiFetch<Quota>("/api/v1/quotas", {
        method: "POST",
        body: { quota_type: quotaType, limit_value: limitValue, period },
      });
      setNotice("Quota saved.");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save quota");
    } finally {
      setBusy(false);
    }
  }

  const columns: Column<Quota>[] = [
    { key: "quota_type", label: "Type", render: (q) => <span className="font-medium">{q.quota_type}</span> },
    { key: "limit_value", label: "Limit", render: (q) => String(q.limit_value) },
    { key: "period", label: "Period" },
    { key: "id", label: "ID", render: (q) => <ShortId id={q.id} /> },
  ];

  return (
    <div>
      <div className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
        <div className="mb-3 text-[13px] font-medium">Upsert quota</div>
        <form onSubmit={upsert} className="grid grid-cols-1 gap-3 md:grid-cols-4">
          <Field label="Quota type">
            <select value={quotaType} onChange={(e) => setQuotaType(e.target.value)} className={inputCls}>
              <option value="tokens_per_day">tokens_per_day</option>
              <option value="requests_per_day">requests_per_day</option>
              <option value="concurrent_requests">concurrent_requests</option>
            </select>
          </Field>
          <Field label="Limit">
            <input type="number" min={0} value={limitValue} onChange={(e) => setLimitValue(Number(e.target.value))} className={inputCls} />
          </Field>
          <Field label="Period">
            <select value={period} onChange={(e) => setPeriod(e.target.value)} className={inputCls}>
              <option value="daily">daily</option>
              <option value="monthly">monthly</option>
            </select>
          </Field>
          <div className="flex items-end">
            <button type="submit" disabled={busy} className={btnCls}>
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </form>
      </div>

      <ErrorBanner msg={error} />
      <Notice msg={notice} />

      <DataTable columns={columns} rows={rows} loading={loading} empty="No quotas yet." />
    </div>
  );
}
