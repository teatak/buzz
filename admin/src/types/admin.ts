export type View = "dashboard" | "users" | "userDetail" | "myKeys" | "providers" | "models";

export type Session = { user: AppUser; version?: string };
export type AppUser = {
  id: number;
  username: string;
  role: string;
  valid: boolean;
  weekly_quota_credits: number;
  lifetime_quota_credits: number;
  quota_anchor_at: string;
  created_at: string;
};
export type UserQuotaStatus = {
  weekly_quota_credits: number;
  weekly_used_microcredits: number;
  weekly_remaining_microcredits: number;
  lifetime_quota_credits: number;
  lifetime_used_microcredits: number;
  lifetime_remaining_microcredits: number;
  period_start: string;
  period_end: string;
  unlimited: boolean;
};
export type UserAPIKey = { id: number; user_id: number; name: string; token: string; valid: boolean };
export type UserAPIKeyDetails = UserAPIKey & {
  created_at: string;
  last_used_at?: string;
  requests: number;
  total_tokens: number;
};

export type ProviderRecord = {
  id: number;
  name: string;
  preset_id: string;
  endpoints: ProviderEndpoint[];
  enabled: boolean;
};

export type ProviderEndpoint = {
  id: number;
  provider_id: number;
  protocol: string;
  base_url: string;
  enabled: boolean;
};

export type ProviderPreset = {
  id: string;
  name: string;
  endpoints: Array<Pick<ProviderEndpoint, "protocol" | "base_url">>;
  description: string;
};

export type ProviderKey = {
  id: number;
  provider_id: number;
  provider_name?: string;
  name: string;
  secret: string;
  secret_hint: string;
  enabled: boolean;
  priority: number;
  weight: number;
  labels?: string;
  disabled_status?: number;
  disabled_error_code?: string;
  disabled_error_message?: string;
  disabled_error_body?: string;
  disabled_at?: string;
};

export type Model = {
  id: number;
  name: string;
  display_name: string;
  icon: string;
  description: string;
  context_window: number;
  max_input_tokens: number;
  max_output_tokens: number;
  quota_uncached_input_rate: number;
  quota_cached_input_rate: number;
  quota_output_rate: number;
  capabilities: string;
  selection_policy: string;
  enabled: boolean;
};

export type ModelPreset = {
  id: string;
  family: string;
  name: string;
  display_name: string;
  description: string;
  context_window: number;
  max_input_tokens: number;
  max_output_tokens: number;
  capabilities: string;
  selection_policy: string;
};

export type ModelRoute = {
  id: number;
  model_id: number;
  provider_id: number;
  provider_name?: string;
  upstream_protocol: string;
  upstream_model: string;
  body_override?: string;
  enabled: boolean;
  priority: number;
  weight: number;
};

export type KeyError = { key: string; model: string; status: number; message: string; updated_at: string };

export type AdminConfig = {
  addr: string;
  upstream_base_url: string;
  timeout: string;
  max_attempts: number;
  cooldown_seconds: number;
};

export type AdminData = {
  config: AdminConfig;
  users: AppUser[];
  user_api_keys: UserAPIKey[];
};

export type Stats = {
  started_at: string;
  requests: number;
  by_key: Record<string, number>;
  exhausted: Record<string, string>;
  rpd_like: Record<string, boolean>;
  key_errors: Record<string, KeyError>;
  last_updated: string;
};

export type UsageSummary = {
  requests: number;
  errors: number;
  avg_latency_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
  by_key: Record<string, number>;
  series: UsagePoint[];
  bucket_minutes: number;
  time_zone?: string;
};

export type UsagePoint = {
  date: string;
  label?: string;
  tooltip?: string;
  requests: number;
  errors: number;
  avg_latency_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
};

// Only known endpoint/preset facts are present; false is distinct from unknown.
export type ModelMetadata = {
  context_window?: number;
  max_input_tokens?: number;
  max_output_tokens?: number;
  quota_uncached_input_rate?: number;
  quota_cached_input_rate?: number;
  quota_output_rate?: number;
  capabilities?: Record<string, boolean>;
};

export type UpstreamModel = ModelMetadata & { id: string; name?: string };
