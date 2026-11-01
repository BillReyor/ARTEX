// Parsing tool for channel field tables and configuration values.
//
// It is separated from the page because this one is **data** rather than a view: it describes what fields each channel has,
// What controls should be used for each, and the two-way conversion of form text to configuration value (JSON).
// After placing a separate file, you only need to move here to add a new channel, and the page itself does not need to be changed.
// The display name and introduction of the channel type. It is placed on the front end because it only affects the copywriting and the back end does not need to know.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "DingTalk",
  feishu: "Feishu",
  wecom: "WeChat Work",
  webhook: "Generic Webhook",
  telegram: "Telegram",
  email: "Email",
};

// Configuration field definitions for each channel.
//
// Here we deliberately keep a front-end field table instead of letting the back-end issue the schema: the back-end is only responsible for
// Validate (required/format), the UI needs layout and control type, and the two are not concerned with the same thing.
// The only coupling point is secret_keys - which fields should be rendered into password boxes are given by the backend,
// Because only the channel implementation knows which values count as credentials (the entire Webhook of Enterprise WeChat is the credentials,
// And Dingding is only one of the secrets). When adding a new channel, missing an entry here will only make the form blank.
// There will be no silent error (hasFields below will prompt).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook Address",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "Signature key",
      kind: "password",
      help: "Fill when bot security setting is 'Signature'; leave empty if 'Custom keyword' is selected or security is disabled.",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook Address",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "Signature verification key", kind: "password", help: "Fill when bot enables 'Signature verification', otherwise empty." },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook Address",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "Target URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "Request method",
      kind: "select",
      options: [
        { value: "POST", label: "POST (With request body)" },
        { value: "PUT", label: "PUT (With request body)" },
        { value: "PATCH", label: "PATCH (With request body)" },
        { value: "GET", label: "GET (Without request body)" },
      ],
    },
    { key: "headers", label: "Custom request header", kind: "kv", help: "“Custom request header”, kind: “kv”, help: “One per line KEY=VALUE, e.g.” Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "Request body template",
      kind: "textarea",
      help:
        "Leave empty to use built-in default template. Variables: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "Also under range .Items .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel." +
        "Use for inserting strings {{json .Xxx}} rather than {{.Xxx}}, Otherwise quotes in the title will break JSON.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API Address",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "Leave empty for official address; fill when using self-hosted Bot API proxy.",
    },
  ],
  email: [
    { key: "host", label: "SMTP Server", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "Port",
      kind: "number",
      placeholder: "587",
      help: "587 Use STARTTLS; enable 'Implicit TLS' for port 465.",
    },
    { key: "username", label: "Account", kind: "text" },
    { key: "password", label: "Password / Auth Code", kind: "password" },
    { key: "from", label: "Sender", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "Recipient", kind: "list", help: "Separate multiple addresses with commas" },
    { key: "tls", label: "Implicit TLS", kind: "switch", help: "465 Port open; keep 587 closed (auto) STARTTLS)" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "No limit" },
  { value: "low", label: "Low or higher risk" },
  { value: "medium", label: "Medium or higher risk" },
  { value: "high", label: "High or higher risk" },
  { value: "critical", label: "Severe only" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV parses the text field of "KEY=VALUE per line".
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs Parses a comma/whitespace separated list of ids.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords parses a line/comma-separated list of keywords (the vulnerability type name may contain spaces, so cut by line or comma).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
