// OmniGate plugin SDK v1 type declarations (served at /api/plugins/sdk.d.ts
// and loaded into the online editor). Runtime: ES2017 in a sandbox without
// require/process/timers; all host access goes through the global `og`.
// Billing meters (computeUnits) see only og.log, og.encoding and og.crypto.sha256.

declare module "@omnigate/plugin-sdk" {
  export interface ChannelInfo {
    id: string
    name: string
    /** The channel's base URL, e.g. "https://api.deepseek.com/v1". */
    baseUrl: string
    models: Array<{ model: string; upstreamModel: string }>
  }

  export interface Ctx {
    channel: ChannelInfo
    /** Non-secret plugin configuration (validated against configSchema). */
    config: Record<string, unknown>
    /** Name of the capability being executed ("" for hooks). */
    capability: string
    /** ISO 8601 timestamp of the call. */
    now: string
  }

  export interface Unsupported {
    unsupported: true
    reason: string
  }

  export interface BalanceOutput {
    currency: string
    /** Decimal string, e.g. "12.34". */
    total: string
    granted?: string
    toppedUp?: string
    available: boolean
  }

  export interface QuotaOutput {
    windows: Array<{ id: string; label: string; used: string; limit: string; resetsAt?: string }>
  }

  export interface ModelsOutput {
    models: Array<{ id: string; displayName?: string }>
  }

  export interface UsageOutput {
    periods: Array<{ label: string; requests?: number; inputTokens?: number; outputTokens?: number; cost?: string; currency?: string }>
  }

  export interface HealthOutput {
    ok: boolean
    latencyMs?: number
    message?: string
  }

  type Out<T> = T | Unsupported | Promise<T | Unsupported>
  type Capability<T> = (input: unknown, ctx: Ctx) => Out<T>

  export interface UpstreamRequest {
    dialect: "openai.chat" | "openai.responses" | "anthropic.messages" | "openai.embeddings"
      | "openai.images.generations" | "openai.images.edits" | "openai.images.variations"
    /** Path relative to the channel base URL; must start with "/". */
    path: string
    /** Header values may contain og.secret() handles. */
    headers: Record<string, string>
    /** JSON body; `{}` for multipart requests (image edits / variations), which only run signRequest (a returned body is ignored). */
    body: any
  }

  // ---- custom upstream protocol (manifest protocol: "custom") ----

  export interface ChatToolCall {
    /** Position of the call in the message (streaming deltas: required to merge fragments). */
    index?: number
    id?: string
    type?: "function"
    function: { name?: string; arguments: string }
  }

  export interface ChatMessage {
    role: "system" | "developer" | "user" | "assistant" | "tool"
    /** string, an array of parts ({type: "text", text} / {type: "image_url", image_url: {url}}) or null. */
    content: string | Array<{ type: string; text?: string; image_url?: { url: string; detail?: string } }> | null
    name?: string
    tool_calls?: ChatToolCall[]
    tool_call_id?: string
    reasoning_content?: string
  }

  /**
   * The gateway's pivot format: an OpenAI Chat Completions request. Messages and
   * Responses clients are converted to it before buildRequest; `model` is the
   * channel's upstream model and `stream` tells whether the client streams.
   */
  export interface CanonicalRequest {
    model: string
    messages: ChatMessage[]
    stream?: boolean
    stream_options?: { include_usage: boolean }
    max_tokens?: number
    max_completion_tokens?: number
    temperature?: number
    top_p?: number
    stop?: string | string[]
    tools?: Array<{ type: "function"; function: { name: string; description?: string; parameters?: any } }>
    tool_choice?: any
    parallel_tool_calls?: boolean
    reasoning_effort?: string
    user?: string
    [extra: string]: any
  }

  /** buildRequest's result (signRequest receives it with dialect "custom"). */
  export interface CustomUpstreamRequest {
    /** Default POST. */
    method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE"
    /**
     * A path starting with "/" is appended to the channel base URL; an absolute
     * https URL must be on the base URL's host or a host in permissions.network.
     */
    url: string
    /** Header values may contain og.secret() handles. No auth header is added by the host. */
    headers?: Record<string, string>
    /** A string is sent as-is; any other value is JSON-encoded (Content-Type defaults to application/json). */
    body?: unknown
    dialect?: "custom"
  }

  /** What parseResponse / normalizeError receive. */
  export interface UpstreamResponse {
    status: number
    /** Lower-cased header names. */
    headers: Record<string, string>
    /** The response body as UTF-8 text (JSON.parse it as needed). */
    body: string
  }

  /** Chat Completions finish_reason. */
  export type FinishReason = "stop" | "length" | "tool_calls" | "content_filter"

  /** Chat Completions usage. */
  export interface ChatUsage {
    prompt_tokens: number
    completion_tokens: number
    total_tokens?: number
    prompt_tokens_details?: { cached_tokens?: number }
    completion_tokens_details?: { reasoning_tokens?: number }
  }

  /**
   * parseResponse's result: a Chat Completions response subset. Missing id,
   * object, created, model, choice index and role are filled in by the host.
   * Return `{error: {status, message}}` instead for an upstream error carried
   * in a successful HTTP response (classified like normalizeError).
   */
  export interface CanonicalResponse {
    id?: string
    choices?: Array<{
      index?: number
      message: { role?: "assistant"; content: string | null; reasoning_content?: string; tool_calls?: ChatToolCall[] }
      finish_reason?: FinishReason | null
    }>
    usage?: ChatUsage
    error?: { status?: number; message: string }
  }

  /** Stream events (a subset of Chat Completions chunks); the host converts them for the client. */
  export type CanonicalEvent =
    | { type: "delta"; content?: string; reasoning?: string; toolCalls?: ChatToolCall[] }
    | { type: "finish"; reason: FinishReason }
    | { type: "usage"; usage: ChatUsage }
    /** status (optional) classifies an error before the first byte reached the client (default 502). */
    | { type: "error"; message: string; status?: number }

  /** Mutable per-request object kept by the host between parseStream calls (same runtime). */
  export type StreamState = Record<string, any>

  // ---- billing plugins (kind includes "billing") ----

  /** Normalized usage of a finished request. */
  export interface Usage {
    /** Input tokens excluding cache reads / writes. */
    input: number
    output: number
    cacheRead: number
    cacheWrite: number
    /** Reasoning tokens (already included in output). */
    reasoning: number
    /** Usage was estimated (the upstream reported none). */
    estimated: boolean
    imageInputTokens?: number
    /** Output images of the image endpoints. */
    images?: number
    [extra: string]: any
  }

  export interface BillingCtx {
    /** Requested (logical) model. */
    model: string
    /** Model that served the request (differs after a route-rule fallback). */
    servedModel: string
    channelId: string
    channelTier: "own" | "shared" | "platform" | string
    /** Name of the user's group ("" when unknown). */
    userGroup: string
    /** Inbound protocol, e.g. "openai.chat", "anthropic.messages". */
    inbound: string
    imageCount: number
    audioSeconds: number
  }

  export interface MeterDefinition {
    /** Optional here: the meter's label / unit are declared in manifest.json billing.meters. */
    label?: string
    unit?: string
    /** Pure and synchronous: no og.fetch, og.storage or og.secret; 5 ms timeout; a non-negative number or decimal string. */
    computeUnits(usage: Usage, ctx: BillingCtx): number | string
  }

  export interface Capabilities {
    "balance.get"?: Capability<BalanceOutput>
    "quota.get"?: Capability<QuotaOutput>
    "models.list"?: Capability<ModelsOutput>
    "usage.query"?: Capability<UsageOutput>
    "health.check"?: Capability<HealthOutput>
    [custom: `custom.${string}`]: Capability<unknown> | undefined
  }

  export interface PluginDefinition {
    capabilities?: Capabilities
    /** Runs before signRequest; may rewrite path, headers and body (50 ms budget). */
    transformRequest?(req: UpstreamRequest, ctx: Ctx): UpstreamRequest
    /** Runs last; when declared the host does not add its own auth header. Custom protocols get buildRequest's result. */
    signRequest?<T extends UpstreamRequest | CustomUpstreamRequest>(req: T, ctx: Ctx): T
    /** Migrates channel config when a channel is upgraded to this version. */
    migrateConfig?(oldConfig: Record<string, unknown>, fromVersion: string): Record<string, unknown>

    // ---- custom protocol (manifest protocol: "custom"; declare them in manifest hooks) ----
    /** Required. Canonical (Chat Completions) request → upstream HTTP request (50 ms). signRequest runs after it. */
    buildRequest?(req: CanonicalRequest, ctx: Ctx): CustomUpstreamRequest
    /** Required. Successful unary response → Chat Completions response (50 ms + 10 ms per 64 KiB, ≤ 1 s). */
    parseResponse?(res: UpstreamResponse, ctx: Ctx): CanonicalResponse
    /** Required. Called once per upstream byte chunk (50 ms each; 5 s of JS per request in total). */
    parseStream?(chunk: Uint8Array, state: StreamState, ctx: Ctx): CanonicalEvent[]
    /** Optional. Called when the upstream stream ends, to flush buffered events. */
    endStream?(state: StreamState, ctx: Ctx): CanonicalEvent[]
    /** Optional. Error response (non-2xx) → gateway error; the status decides retry classification. */
    normalizeError?(res: UpstreamResponse, ctx: Ctx): { status: number; message: string }

    // ---- billing plugins ----
    billing?: { meters: Record<string, MeterDefinition> }
  }

  export function definePlugin(def: PluginDefinition): PluginDefinition
}

interface OgResponse {
  status: number
  ok: boolean
  /** Lower-cased header names. */
  headers: Record<string, string>
  text(): string
  json(): any
}

interface OgFetchInit {
  method?: string
  headers?: Record<string, string>
  /** String bodies are sent as-is; objects are JSON-encoded. */
  body?: string | object
  /** Default 10000, max 30000. */
  timeoutMs?: number
}

declare const og: {
  /** Only https hosts listed in permissions.network (or "$baseUrl"); no redirects; ≤ 4 MiB; ≤ 10 calls. */
  fetch(url: string, init?: OgFetchInit): Promise<OgResponse>
  /** Opaque handle replaced by the host inside og.fetch and hook headers. */
  secret(name: string): string
  crypto: {
    sha256(data: string, encoding?: "hex" | "base64"): string
    /** key may be an og.secret() handle; the plaintext never enters the sandbox. */
    hmacSha256(key: string, data: string, encoding?: "hex" | "base64"): string
  }
  encoding: {
    base64Encode(s: string): string
    base64Decode(s: string): string
  }
  /** Requires permissions.storage. Isolated per (plugin, channel). */
  storage: {
    get(key: string): any
    set(key: string, value: unknown): void
    delete(key: string): void
  }
  log: {
    info(...args: unknown[]): void
    warn(...args: unknown[]): void
    error(...args: unknown[]): void
  }
}

declare const console: {
  log(...args: unknown[]): void
  warn(...args: unknown[]): void
  error(...args: unknown[]): void
}

/** UTF-8 only. decode(chunk, {stream: true}) keeps an incomplete trailing sequence for the next call. */
declare class TextDecoder {
  constructor(label?: "utf-8" | "utf8")
  readonly encoding: "utf-8"
  decode(input?: Uint8Array | ArrayBuffer, options?: { stream?: boolean }): string
}

declare class TextEncoder {
  readonly encoding: "utf-8"
  encode(input?: string): Uint8Array
}
