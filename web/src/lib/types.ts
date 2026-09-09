// TypeScript mirrors of the Go control-plane JSON payloads
// (see go-server/internal/controlplane/*.go) + JWT claims (auth/jwt.go).

export type Role = "PLATFORM_ADMIN" | "TENANT_ADMIN" | "TENANT_DEVELOPER" | "TENANT_VIEWER";

export interface Claims {
  uid: string;
  tid: string;
  role: Role;
  exp?: number;
  iat?: number;
  jti?: string;
}

export interface Tenant {
  id: string;
  name: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface Model {
  id: string;
  name: string;
  description: string;
  task: string;
  framework: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface ModelVersion {
  id: string;
  model_id: string;
  version: string;
  artifact_uri: string;
  metadata: Record<string, unknown>;
  status: string;
  created_at: string;
}

export interface ServingTemplate {
  id: string;
  name: string;
  description: string;
  runtime: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface TemplateVersion {
  id: string;
  template_id: string;
  version: string;
  image: string;
  command?: string[];
  environment?: Record<string, string>;
  config_schema?: Record<string, unknown>;
  created_at: string;
}

export type DeploymentStatus =
  | "PENDING"
  | "PROVISIONING"
  | "STARTING"
  | "READY"
  | "STOPPING"
  | "STOPPED"
  | "FAILED"
  | "DEGRADED";

export interface Deployment {
  id: string;
  tenant_id: string;
  model_version_id: string;
  template_version_id: string;
  name: string;
  region: string;
  desired_replicas: number;
  status: DeploymentStatus;
  workload_ref?: string;
  created_at: string;
  updated_at: string;
}

export interface APIKey {
  id: string;
  tenant_id: string;
  name: string;
  status: string;
  expires_at?: string;
  created_at: string;
}

export interface Quota {
  id: string;
  tenant_id: string;
  quota_type: string;
  limit_value: number;
  period: string;
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}

export interface SessionSummary {
  id: string;
  title: string;
  model: string;
  created_at: string;
  updated_at: string;
  message_count: number;
}

export interface UsageSummary {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  requests: number;
}

export interface UsageDailyPoint {
  date: string;
  prompt_tokens: number;
  completion_tokens: number;
  requests: number;
}

export interface UsageByModel {
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  requests: number;
}

export interface UsageResponse {
  today: UsageSummary;
  month: UsageSummary;
  daily: UsageDailyPoint[];
  by_model: UsageByModel[];
}
