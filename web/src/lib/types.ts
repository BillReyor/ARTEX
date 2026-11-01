// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: Key data shapes).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // Optional task name; empty/default = unnamed, fallback to description when displaying
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // Number of registered vulnerabilities (classified by severity)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // Asset coverage function switch (set at creation time, enabled by default); false=do not calculate/do not display coverage
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // Default category; null/default=none
  intercept_rules?: AssetInterceptRuleInput[]; // Preset task-level block/allow rules
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // Vested company asset id; empty = not vested
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// Force directed to a node in the Asset Overlay. Key unique: asset="a:<id>", company="c:<id>",
// Root domain name="r:<domain>" for no asset row. In_scope=false is a gray context node used only for wires.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// The intent/fact/finding associated with an asset in this task exploration graph (used in the overlay graph node drawer).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// A line of task test scope (coverage denominator + authorization boundary).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // Backend JOIN companies parsing, only kind=company has value
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Structured asset scope rules submitted when adding a new enterprise.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// The result of asset scope writing. errors are illegal lines in this submission; warnings are irrelevant to this submission,
// However, there are existing data problems that will make the attribution results not meet expectations (such as assets with host names stored in the IP field).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// One of the company's asset range rules (attribution to a single source of truth).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // There is a value when kind=domain
  net?: string; // kind=ip|cidr has value when
  value?: string; // When kind=icp|keyword, it may be returned directly by the backend
  raw: string; // Raw user input, used for display and backfill
  reason?: string;
}

// Enterprise: Asset node + icon + asset count + asset range rule for type=company.
export interface Company {
  id: number;
  name: string;
  logo?: string; // Remote icon URL; if empty, the front end uses the first letter of the name
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // Reason for deletion when false deletion is intended (state='deleted')
}

// One page of the bulletin board: nodes paginated in order of creation + the edges involved in this page + the nodes at the other end of the edges (refs, indexed by id),
// In this way, each broadcast can clearly explain "where it comes from and what it produces" without having to pull down the entire picture.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // Node id → the asset anchored by the node (displayed when the bulletin board is expanded, including the node on this page and its neighbors).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// The target used by the target management card (the backend has split the payload into text/vulnclass).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// Operational constraints used by constraint management cards (allow=allow / deny=deny).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Vulnerability handling status: pending/processing/confirmed/handled/fixed/false positive/ignore/duplicate/risk accepted.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset is a vulnerability-bound asset (the label has been pre-rendered on the backend).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // The row id of the independent findings table, the handle of the status update (old nodes in the task may be missing)
  vulnclass: string;
  name?: string; // Vulnerability name; when empty, the display falls back to vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // Detailed report (Markdown); only the details interface is returned, the list is empty
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage is the server-side paginated response of the discovery list.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // Optional task name; empty/default = unnamed
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats is a full table aggregation (statistics card + vulnerability type drop-down), calculated on the server side, and is not affected by paging.
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption is an item from the "By Task" filter drop-down on the discovery page: tasks with vulnerabilities (an empty description indicates that the task has been deleted.
// The front-end rollback displays the ID) and the number of vulnerabilities.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // Optional task name; empty/default = unnamed
  description: string;
  count: number;
}

// FindingQuery is the discovery list paging/filtering/sorting parameter.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // Task id;"all"/empty = do not filter by task
  query?: string;
  sort?: "severity" | "time";
  // Asset tree node key; select a node = select its entire subtree. Empty = Do not filter by asset.
  assetScope?: string;
}

// ---- Findings by asset (asset view) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode is a node in the asset tree. The key is in the form of a:<id>(asset), c:<id>(enterprise),
// r:<domain> (there is no root domain name of the asset row in the library), __none__ (no assets are associated).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // The number of discoveries directly linked to this asset
  total: number; // Include descendants, remove duplicates according to discovery
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET corresponds to the backend db.FindingUnassignedAsset.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment is an uploaded file: the path is relative to the session/task working directory (that is, the agent's CWD).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // Absolute path (scope=staging is returned when temporarily uploading; write it into the description before creating the task)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 scheduling, only custom agents) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // Timing: every N seconds (0=unscheduled)
  on_finding: boolean; // Triggered when any task finds finding
  on_goal_met: boolean; // Triggered when any task reaches its goal
  on_task_timeout: boolean; // Triggered when any task times out
  on_tool_call: boolean; // Triggered when the selected tool is called (execution is completed)
  on_task_create: boolean; // Triggered when any task is created
  interval_message: string; // Independent user messages for each trigger condition
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call selected tool key (at least one)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// Task-by-task results of batch reclassification. Failure is only possible if the task has been deleted, the writing of the category itself is atomic.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent automatically binds traffic evidence, which is turned off by default; does not affect manual binding.
  llm_record: boolean; // LLM recording switch (off by default); no LLM calls are recorded when off
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // Standalone egress proxy (http/https/socks5) for accessing the search endpoint; independent of the MITM proxy that logs traffic. Empty = direct connection.
  web_search_proxy?: string;
  // Global export proxy (http/https/socks5, user:pass available), all target traffic goes through it. When traffic capture is turned on as
  // MITM upstream; bash/WebFetch that injects the agent directly when capture is turned off. Empty = direct connection.
  global_proxy?: string;
  python_interpreter?: string; // Python interpreter path for custom script tools (empty = runtime detection)
  workers?: number; // Number of concurrent work agents (default 3); takes effect for tasks started later
  // Task concurrency upper limit: the upper limit of the number of tasks that are "running" at the same time. Off = no limit; after it is enabled, new tasks will be queued if they exceed the limit, and they will be automatically started if there is space.
  task_concurrency_enabled?: boolean; // Default false
  task_concurrency_limit?: number; // Default is 5 after opening
  // LLM polling (failover). Off by default; when turned on, agents with "unspecified models" are unavailable in the current configuration.
  // (Insufficient balance/key invalid/current limiting/service abnormality) Automatically switch to the next configuration.
  llm_pool_enabled?: boolean; // Default false
  // Whether the agent/task bound to the specified configuration will also fall back to the polling chain when it fails. Default false = bound is exclusive.
  llm_pool_bind_fallback?: boolean;
  // Operation constraint injection scope (both enabled by default): Put the allow/deny constraints of the task into the system prompt of the corresponding agent.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // Experimental feature: noa model-driven context compression (off by default). The four types of agents (planner/
  // worker/main agent/conversation), noa takes over the context compression, replacing the built-in compaction; it is read once per run, and the
  // Runs started later take effect.
  noa_compaction?: boolean;
  // ---- Vulnerability IM push (the channel itself is an independent resource, see /api/notify/*, there are only three global configurations here) ----
  notify_enabled?: boolean; // Push main switch, on by default; used to stop bleeding with one click during maintenance period
  notify_public_base_url?: string; // The external access address of the vulnerability details return link; empty = the message does not return the link
  notify_digest_interval_min?: number; // Summary mode period (minutes), default 30
}

// ---- Vulnerability IM push ----

// NotificationFilter is the filter condition of the channel, all fields are optional, and the default is no filtering.
// The backend does not verify all fields: when the configuration is malformed, it is treated as a "hit" (it is better to push more than to miss).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // Empty = no limit; if not empty, it requires intersection with the task to which the vulnerability belongs.
  asset_ids?: number[]; // Empty = no limit; if not empty, it requires an intersection with the vulnerability-anchored asset
  vulnclass_include?: string[]; // Empty = accept all; non-empty requires the vulnerability type to hit any keyword (case-insensitive substring)
  vulnclass_exclude?: string[]; // Hit any keyword to exclude (exclusion takes precedence over inclusion)
  on_status_change?: boolean; // Whether to also receive vulnerability handling status change events
}

// NotificationChannel is a channel instance. The fields of config vary with kind,
// And the credential field is replaced with the masked value starting with "__masked__" when reading - returning it as it is means "no change".
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys are given by the backend according to the channel type, and the frontend renders the password box and the "leave blank or do not change" prompt accordingly.
  // No channel knowledge is hard-coded.
  secret_keys: string[];
}

// NotificationKind is the channel type metadata returned by /api/notify/meta.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery is a delivery record used for delivery history and failed retransmission.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // Thinking switch (thinking.type): ""=Do not send (default) | "disabled"=Close | "enabled"=Open
  thinking_type?: string;
  // Thought intensity: ""=Do not send (default) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // Polling order: The larger the value, the first to be selected. The active configuration is always the head of the chain and has nothing to do with this value.
  priority?: number;
  // true = Not used as a failover target (can still be used by explicit agent/task bindings).
  pool_exclude?: boolean;
  // true (default) = streaming (SSE) | false = true non-streaming (stream:false, returned once).
  streaming?: boolean;
  // The upper limit of output (token) for a single reply. 0 = Do not send this field, determined by the server's default value.
  // Note the difference with context_window_k: the latter is the total capacity of the model and is only used locally for compression thresholds.
  max_tokens?: number;
  // Which request field name is used for the upper limit? Only format="openai" is meaningful:
  // ""=max_tokens (default) | "max_completion_tokens" (OpenAI inference model only recognizes it)
  max_tokens_field?: string;
  // Custom session header name: This HTTP header will be included in each request when it is not empty, and the header value = the session id of the current session/intent.
  // ""=Do not send. Gateway for prompt caching/sticky routing based on session-id header.
  session_header_key?: string;
  // This configuration covers retries (connection establishment/empty response/same provider security window). Leave empty/all 0 = follow global policy.
  retry?: LLMRetryOverride;
}

// ---- LLM retry strategy ----
// One layer of two knobs to retry. Both are "0 = not configured":
// attempts 0=use the default number of attempts | -1=turn off the layer and try again | >0=number of retries
// interval_ms 0=use the default exponential backoff | >0=use this fixed millisecond interval instead
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// There are three layers that a single LLM configuration can cover (all are "follow the endpoint" retries).
export interface LLMRetryOverride {
  connect: LLMRetryRule; // Connection establishment retry: connection reset/timeout/429/5xx, before stream starts
  empty: LLMRetryRule; // Empty response retry: completed with no content (openai format only)
  stream: LLMRetryRule; // Same as provider safe window retry: stream replay before output is delivered
}

// Global policy = the default values of the above three layers + the two layers are only global:
// breaker polling fuse (attempts=several consecutive instantaneous fuse failures, interval_ms=fixed cooling time)
// intent rerun (the entire intent is rerun after the worker ends with model_error)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM polling (failover)----
// A configuration's position and health status in the polling chain. state:
// ok normal
// degraded There are consecutive failures but the circuit breaker threshold is not reached
// tripped has been blown and will be skipped during the cooling period (cooldown_secs is the remaining seconds)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // Whether it is the currently active configuration (always the head of the chain)
  excluded: boolean; // pool_exclude: do not participate in polling
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // Built-in as goals/planner/mainagent/worker; customized as user-defined key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // Bind LLM configuration; null/absent = follow task/session/global
  max_turns?: number; // 0 = no limit
  run_seconds?: number; // The maximum wall clock limit for a single worker run (seconds); 0 = no limit
  web_search?: boolean; // Whether to enable network search (gated by the system global switch)
  interactive_shell?: boolean; // Whether to enable interactive shell (persistent PTY session tool family)
  // P3 post-trigger processing strategy (only meaningful for custom agents)
  trigger_run_mode?: "serial" | "parallel"; // Serial queuing / each trigger has a concurrent session
  trigger_merge_mode?: "by_task" | "all" | "none"; // Serial only: merge with same tasks/merge all/no merge
  trigger_max_parallel?: number; // Parallel only: the upper limit of concurrency per agent; 0=no limit
  // Number of bindings (only returned by list interface): Visible MCP / Visible Skill / Binding Tool
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // Bindable LLM configuration candidates (for "Default Model" drop-down); see agent.llm_profile_id for current bindings
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // Saved closing prompt word (empty = use built-in default)
  wrapup_default?: string; // Built-in default closing prompt word (placeholder/restore default)
  wrapup_max_turns?: number; // Number of closing rounds saved (0=use built-in default)
  wrapup_max_turns_default?: number; // Built-in default closing round number (for "0=default N" prompt)
  // Task-level timeout ending word (only worker/planner, this partition is displayed when task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // Call statistics (skill_usage ledger). Skills that have never been called: calls=0, last_used Default.
  calls: number;
  tasks: number; // The number of tasks it has been loaded into (chat sessions are not counted)
  usage_agents: string[]; // its agent key has been loaded
  last_used?: string;
}

// A SkillCall is a Skill() call (a list of recent calls to a single skill).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = non-mission scenario (dialogue session)
  session_id: string;
  args_len: number;
}

// MissingSkill is a skill that is named but does not exist - the "want to use but don't have it" gap.
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (built-in tool directory) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // Custom tool type
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // Custom tool execution specifications (kind!=builtin)
  deferred?: boolean; // schema delay (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules (Asset Intercept: Global Blacklist) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action is only used for task-level rules: block=interception (forbidden testing) allow=allow (whitelist).
export type AssetInterceptAction = "block" | "allow";

// Entry items for task-level asset blocking/allowing rules (used when creating tasks and editing task details).
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // Global rules do not have this field (always intercept); task-level rules distinguish block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // Rule message or model judgment reason (model judgment is prefixed with [model])
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: Global configuration of model approval (only judged by the model when no interception rules are hit).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = follow active/default configuration
  prompt: string; // Determination prompt word; GET backend backfills the full text of the built-in template when it is not set
  timeout_seconds: number; // Model call timeout
  fail_action: "allow" | "ask" | "deny"; // Fallback when model errors/times out/cannot be parsed
  ask_timeout_seconds: number; // The approval waiting timeout after model evaluation ask is transferred to manual
  ask_timeout_action: "allow" | "deny"; // Default action after approval timeout
}

// JudgeUsage: The cumulative token usage of the model’s bottom-line approval (judge channel) + the daily sequence in the past N days.
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── Asset synchronization (ScopeSentry data source) ───────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// Call statistics of a single tool (/commands/stats); errors are the number of failures.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // The original HTTP text actually sent and received by the provider: The request is the complete body (including tools) sent by buildBody()
  // schema), the response is the original SSE frame. The above request_body/response_body is a normalized view,
  // The tool schema and tool_use blocks have been dropped. The old record is empty.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/* * GET /api/update/check - Comparison between the current version and the latest official version from GitHub. */
export interface UpdateCheck {
  /* * The currently running version; development builds are "dev" or the suffixed form of git describe. */
  current: string;
  /* * Running form. Reinstallation under docker only affects the writable layer of the container, and rebuilding the container will return the image version. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /* * Whether there is a previous version (artex.old) that can be rolled back. */
  has_backup: boolean;
  /* * The conclusion of the self-updated bootstrapping during this startup (replacement failed/rolled back, etc.) will be empty when nothing happens. */
  boot_notice?: string;
  rolled_back?: boolean;
  /* * Give the reason when querying GitHub fails. At this time, none of the following fields will be available. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /* * The release package name corresponding to the current platform, and whether this Release actually comes with it. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /* * Whether the version numbers of both sides are comparable; for development builds, it is false, and one-click updates are disabled at this time. */
  comparable?: boolean;
  /* * Description when comparable is false. */
  reason?: string;
}

/* * An update progress pushed by /api/update/stream. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /* * Only the download phase is meaningful (0-100); the remaining phases are -1. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}
