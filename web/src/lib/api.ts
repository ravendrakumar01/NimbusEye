/**
 * Typed client for the NimbusEye API.
 *
 * Every type here mirrors internal/model in the Go service. They are written by
 * hand rather than generated because the surface is small and a generator is one
 * more thing to install; if the API grows much beyond this, generating from an
 * OpenAPI spec becomes the better trade.
 */

export type Status =
  | "down"
  | "critical"
  | "trouble"
  | "up"
  | "maintenance"
  | "suspended"
  | "discovery"
  | "unknown";

export type Severity = "down" | "critical" | "trouble" | "info";
export type AlarmState = "open" | "acknowledged" | "resolved" | "suppressed";
export type Provider = "oci" | "aws" | "azure" | "gcp" | "k8s" | "synthetic";

export interface Resource {
  id: string;
  cloud_account_id: string;
  provider: Provider;
  resource_type: string;
  type_name: string;
  category: string;
  native_id: string;
  display_name: string;
  region: string;
  status: Status;
  status_since: string;
  last_polled_at: string | null;
  suspended: boolean;
  tags: Record<string, string>;
  group_ids: string[] | null;
  attributes: Record<string, unknown>;
  availability_24h: number;
  open_alarms: number;
}

export interface Alarm {
  id: string;
  resource_id: string;
  resource_name: string;
  resource_type: string;
  provider: Provider;
  region: string;
  dedup_key: string;
  severity: Severity;
  state: AlarmState;
  metric_key?: string;
  metric_label?: string;
  unit?: string;
  observed_value?: number;
  threshold_value?: number;
  message: string;
  poll_count: number;
  opened_at: string;
  acknowledged_at?: string;
  acknowledged_by?: string;
  resolved_at?: string;
  escalation_level: number;
}

export interface Outage {
  id: string;
  resource_id: string;
  resource_name: string;
  provider: Provider;
  started_at: string;
  ended_at: string | null;
  duration_sec: number;
  severity: Severity;
  classified_as: "outage" | "maintenance" | "false_positive";
  root_cause?: string;
  comment?: string;
}

export interface CloudAccount {
  id: string;
  provider: Provider;
  display_name: string;
  native_account_id: string;
  regions: string[];
  enabled: boolean;
  resource_count: number;
  last_discovery_at: string | null;
  discovery_state: "never" | "running" | "ok" | "partial" | "failed";
  last_error?: string;
  /** Server-side path to the secret. The secret itself never crosses this API. */
  credentials_ref?: string;
  config?: Record<string, string>;
  discovery_interval_sec?: number;
  metric_interval_sec?: number;
}

export interface FieldSpec {
  key: string;
  label: string;
  placeholder: string;
  help: string;
  required: boolean;
  is_path: boolean;
}

export interface ProviderSpec {
  provider: Provider;
  label: string;
  account_id_label: string;
  account_id_placeholder: string;
  regions: string[];
  fields: FieldSpec[];
  credential: FieldSpec;
  permissions: string[];
}

export interface AccountInput {
  provider: string;
  display_name: string;
  native_account_id: string;
  regions: string[];
  credentials_ref: string;
  config: Record<string, string>;
  enabled?: boolean;
  discovery_interval_sec?: number;
  metric_interval_sec?: number;
}

export interface VerifyCheck {
  name: string;
  status: "ok" | "failed" | "skipped";
  detail?: string;
}

export interface VerifyResult {
  ok: boolean;
  checks: VerifyCheck[];
  message: string;
}

export interface ServiceTile {
  resource_type: string;
  display_name: string;
  icon: string;
  category: string;
  count: number;
  unhealthy: number;
  enabled: boolean;
}

export interface ResourceGroup {
  id: string;
  display_name: string;
  description?: string;
  parent_group_id?: string;
  resource_count: number;
  status: Status;
  unhealthy: number;
}

export interface StatusSummary {
  total: number;
  by_status: Partial<Record<Status, number>>;
  by_provider: Record<string, Partial<Record<Status, number>>>;
  by_category: Record<string, Partial<Record<Status, number>>>;
  open_alarms: Partial<Record<Severity, number>>;
  unacked_alarms: number;
  availability_24h: number;
  ongoing_outages: number;
  generated_at: string;
}

export interface MetricDef {
  key: string;
  label: string;
  unit: string;
  provider_metric: string;
  namespace: string;
  statistic: string;
  trouble: number | null;
  critical: number | null;
  higher_is_worse: boolean;
}

export interface ResourceType {
  code: string;
  provider: Provider;
  category: string;
  display_name: string;
  icon: string;
  supports_availability: boolean;
  supports_metrics: boolean;
  supports_cost: boolean;
  default_poll_sec: number;
  metrics: MetricDef[];
}

export interface Filters {
  providers: string[];
  categories: string[];
  statuses: Status[];
  severities: Severity[];
  resource_types: {
    code: string;
    display_name: string;
    provider: Provider;
    category: string;
    icon: string;
  }[];
  regions: Record<string, string[]>;
  tags: Record<string, string[]>;
  groups: ResourceGroup[];
}

/** Counts over the whole filtered set, not the page and not the whole estate. */
/** A monitor type the user may create directly, with the fields it needs. */
export interface CreatableType {
  code: string;
  display_name: string;
  category: string;
  icon: string;
  provider: Provider;
  fields: FieldSpec[];
  default_poll_sec: number;
  description: string;
}

/** A type that exists but is discovered rather than created, with the reason. */
export interface DiscoveredType {
  code: string;
  display_name: string;
  provider: Provider;
  category: string;
  reason: string;
}

export interface MonitorInput {
  resource_type: string;
  display_name: string;
  target: string;
  check_interval_sec?: number;
  tags?: Record<string, string>;
  method?: string;
  expected_status?: number[];
  match_text?: string;
  follow_redirects?: boolean;
  timeout_sec?: number;
  port?: number;
  record_type?: string;
  resolver?: string;
  expected_ip?: string;
}

export interface ResourceCounts {
  by_status: Partial<Record<Status, number>>;
  total: number;
  maintenance: number;
  discovery: number;
  suspended: number;
  config_errors: number;
  open_alarms: number;
  anomalies: number;
  /** Mean 24h availability over the filtered set; resources with no history are excluded. */
  availability_24h: number;
}

export interface ResourceList {
  items: Resource[];
  total: number;
  page: number;
  page_size: number;
  counts: ResourceCounts;
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface Sample {
  t: string;
  v: number;
}

export interface MetricSeries {
  resource_id: string;
  metric_key: string;
  label: string;
  unit: string;
  trouble?: number;
  critical?: number;
  samples: Sample[];
}

/** Thrown for any non-2xx response, carrying the API's error code. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    /** Present on 422: field name -> message, so a form can mark each input. */
    readonly fields?: Record<string, string>,
    /** Present on a lockout: how long until another attempt is accepted. */
    readonly retryAfter?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const BASE = "/api/v1";

/** The authenticated user, as returned by login and the session endpoint. */
export interface AuthUser {
  id: string;
  tenant_id: string;
  tenant_slug: string;
  email: string;
  display_name: string;
  role: "owner" | "admin" | "operator" | "viewer";
  timezone?: string;
  mfa_enabled: boolean;
}

export interface SessionState {
  authenticated: boolean;
  user?: AuthUser;
  expires_at?: string;
  csrf_token?: string;
}

/**
 * Reads the CSRF cookie.
 *
 * Deliberately not HttpOnly on the server: the double-submit pattern requires the
 * page to read it and echo it in a header, which is what proves the request came
 * from this origin rather than from a cross-site form.
 */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)nimbuseye_csrf=([^;]+)/);
  return match?.[1] ? decodeURIComponent(match[1]) : "";
}

/** Called when the server reports the session is gone, so the shell can react. */
let onUnauthenticated: (() => void) | null = null;

export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn;
}

async function get<T>(path: string, params?: Record<string, unknown>): Promise<T> {
  const url = new URL(BASE + path, window.location.origin);
  for (const [k, v] of Object.entries(params ?? {})) {
    if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0)) {
      continue;
    }
    url.searchParams.set(k, Array.isArray(v) ? v.join(",") : String(v));
  }
  return request<T>(url.toString(), { method: "GET" });
}

async function request<T>(url: string, init: RequestInit): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(init.headers as Record<string, string> | undefined),
  };
  // The header is only required on state-changing methods, which is exactly where
  // the server checks it.
  if (method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = csrfToken();
  }

  const res = await fetch(url, {
    ...init,
    headers,
    // Cookies are the session; without this a cross-origin dev setup would send
    // none and every request would look unauthenticated.
    credentials: "same-origin",
  });

  // A 401 means the session ended — expired, revoked, or the user was disabled.
  // Handled centrally so every page does not need its own redirect.
  if (res.status === 401 && !url.includes("/auth/")) {
    onUnauthenticated?.();
  }
  if (!res.ok) {
    // The API always returns {error:{code,message}}; fall back gracefully if a
    // proxy or nginx returns HTML instead.
    let code = "http_" + res.status;
    let message = res.statusText;
    let fields: Record<string, string> | undefined;
    let retryAfter: number | undefined;
    try {
      const body = (await res.json()) as {
        error?: {
          code: string;
          message: string;
          fields?: Record<string, string>;
          retry_after_seconds?: number;
        };
      };
      if (body.error) {
        code = body.error.code;
        message = body.error.message;
        fields = body.error.fields;
        retryAfter = body.error.retry_after_seconds;
      }
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, code, message, fields, retryAfter);
  }
  return (await res.json()) as T;
}

export interface ResourceQuery {
  q?: string;
  status?: Status[];
  provider?: string[];
  type?: string[];
  category?: string[];
  region?: string[];
  group?: string;
  tag?: string;
  issues?: boolean;
  sort?: "status" | "name" | "availability" | "polled";
  page?: number;
  page_size?: number;
}

export interface AlarmQuery {
  state?: AlarmState[];
  severity?: Severity[];
  provider?: string[];
  resource_id?: string;
  q?: string;
  all?: boolean;
  page?: number;
  page_size?: number;
}

export const api = {
  /** Public: reports whether a session exists, without treating absence as an error. */
  session: () => get<SessionState>("/auth/session"),
  bootstrapStatus: () => get<{ users: number; needs_bootstrap: boolean }>("/auth/bootstrap"),

  login: (email: string, password: string) =>
    request<{ user: AuthUser; expires_at: string; csrf_token: string }>(`${BASE}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password }),
    }),
  logout: () => request<{ signed_out: boolean }>(`${BASE}/auth/logout`, { method: "POST" }),
  forgotPassword: (email: string) =>
    request<{ message: string }>(`${BASE}/auth/forgot`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email }),
    }),
  resetPassword: (token: string, newPassword: string) =>
    request<{ reset: boolean; message: string }>(`${BASE}/auth/reset`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token, new_password: newPassword }),
    }),
  changePassword: (current: string, next: string) =>
    request<{ changed: boolean; message: string }>(`${BASE}/auth/password`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ current_password: current, new_password: next }),
    }),

  /** /healthz sits outside /api/v1, so it is requested directly. */
  health: () =>
    request<{ status: string; version: string; mode: string; time: string }>("/healthz", {
      method: "GET",
    }),

  summary: () => get<StatusSummary>("/status/summary"),
  filters: () => get<Filters>("/catalog/filters"),
  resourceTypes: (provider?: string) =>
    get<{ items: ResourceType[]; total: number }>("/catalog/resource-types", { provider }),

  resources: (q: ResourceQuery = {}) =>
    get<ResourceList>("/resources", { ...q, issues: q.issues ? 1 : undefined }),

  resource: (id: string) =>
    get<{ resource: Resource; type: ResourceType; alarms: Alarm[] }>(`/resources/${id}`),

  metrics: (id: string, opts: { metric?: string[]; from?: string; to?: string; points?: number } = {}) =>
    get<{ resource_id: string; from: string; to: string; series: MetricSeries[] }>(
      `/resources/${id}/metrics`,
      opts,
    ),

  alarms: (q: AlarmQuery = {}) =>
    get<Page<Alarm>>("/alarms", { ...q, all: q.all ? 1 : undefined }),

  acknowledge: (id: string, user?: string) =>
    request<Alarm>(`${BASE}/alarms/${id}/acknowledge`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user }),
    }),

  outages: (q: { resource_id?: string; ongoing?: boolean; page?: number; page_size?: number } = {}) =>
    get<Page<Outage>>("/outages", { ...q, ongoing: q.ongoing ? 1 : undefined }),

  creatableTypes: () =>
    get<{ creatable: CreatableType[]; discovered: DiscoveredType[] }>("/monitors/creatable"),
  createMonitor: (input: MonitorInput) =>
    request<Resource>(`${BASE}/monitors`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  updateMonitor: (id: string, input: MonitorInput) =>
    request<Resource>(`${BASE}/monitors/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  deleteMonitor: (id: string) =>
    request<{ deleted: boolean }>(`${BASE}/monitors/${id}`, { method: "DELETE" }),
  suspendMonitor: (id: string) =>
    request<Resource>(`${BASE}/monitors/${id}/suspend`, { method: "POST" }),
  activateMonitor: (id: string) =>
    request<Resource>(`${BASE}/monitors/${id}/activate`, { method: "POST" }),

  accounts: () => get<{ items: CloudAccount[]; total: number }>("/cloud-accounts"),
  account: (id: string) => get<CloudAccount>(`/cloud-accounts/${id}`),
  providerSpecs: () => get<{ items: ProviderSpec[] }>("/cloud-accounts/providers"),
  accountServices: (id: string) =>
    get<{ items: ServiceTile[]; total: number }>(`/cloud-accounts/${id}/services`),

  createAccount: (input: AccountInput) =>
    request<CloudAccount>(`${BASE}/cloud-accounts`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  updateAccount: (id: string, input: AccountInput) =>
    request<CloudAccount>(`${BASE}/cloud-accounts/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  deleteAccount: (id: string) =>
    request<{ deleted: boolean; resources_affected: number }>(`${BASE}/cloud-accounts/${id}`, {
      method: "DELETE",
    }),
  verifyAccount: (id: string) =>
    request<VerifyResult>(`${BASE}/cloud-accounts/${id}/verify`, { method: "POST" }),
  groups: () => get<{ items: ResourceGroup[]; total: number }>("/groups"),
};
