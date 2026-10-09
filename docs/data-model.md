# 核心数据模型（ER 草案）

> ✅ = 已建表（`00001_identity_audit.sql`、`00002_gateway.sql`、`00003_billing.sql`、`00004_plugins.sql`）；其余为草案，在对应阶段先提交迁移和契约再实现。
> Round 2 实现与草案的差异：价格统一为一张 `prices` 表（`kind = sell | cost`）；用量与成本作为 `request_logs` 的列（不拆 `usage_facts`/`cost_facts`），
> 收入以账本中 `kind='charge'` 的唯一记录为准；`key_policies`、`budgets` 合并为 `gateway_keys.policy` jsonb（预算在 Phase 3）。
> 约定：主键 `uuid`（UUIDv7，由应用生成）；时间 `timestamptz`（UTC）；金额 `bigint`，单位为 nano（ADR-0007）；
> 可共享资源带 `workspace_id`（预留，当前为 NULL）与 `scope`；需要乐观锁的表带 `version`。

## 1. 身份与权限 ✅

```mermaid
erDiagram
  users ||--o{ user_identities : "关联外部身份"
  users ||--o{ sessions : "浏览器会话"
  users ||--o{ audit_logs : "执行者"
  users {
    uuid id PK
    uuid workspace_id "预留"
    text display_name
    text email
    text role "system_admin|channel_admin|user|auditor"
    text status "active|disabled"
    int version
    timestamptz last_login_at
  }
  user_identities {
    uuid id PK
    uuid user_id FK
    text provider "github|<oidc-id>"
    text subject "不可变 ID，UNIQUE(provider,subject)"
    text login
    text email
    bool email_verified
  }
  sessions {
    uuid id PK
    uuid user_id FK
    bytea token_hash "SHA-256，UNIQUE"
    timestamptz expires_at
    timestamptz last_seen_at
    text ip_prefix "/24 或 /48"
    timestamptz revoked_at
  }
  audit_logs {
    uuid id PK
    uuid actor_id
    text action
    text resource_type
    text resource_id
    jsonb metadata "禁止写入密钥"
  }
  system_settings {
    text key PK
    jsonb value
    int version
  }
```

角色与权限映射首期在代码中定义（ADR-0004），因此**不建** `roles`、`permissions` 表。

## 2. 插件 ✅（Round 3；与草案差异：权限审批记在 `plugin_versions.approval*` 列而非独立的 `plugin_permissions` 表；订阅源表 `plugin_subscriptions` 推迟到 Round 4；新增 `capability_results`）

```mermaid
erDiagram
  plugins ||--o{ plugin_versions : ""
  plugin_versions ||--o{ plugin_permissions : "授予的权限"
  plugin_subscriptions ||--o{ plugins : "来源"
  plugins ||--o{ plugin_drafts : "在线编辑"
  plugins {
    uuid id PK
    text plugin_key "community.deepseek"
    text status "active|disabled"
    uuid installed_by
  }
  plugin_versions {
    uuid id PK
    uuid plugin_id FK
    text version
    bytea content_hash
    text signature_status
    text source
    jsonb manifest
    bytea bundle
    timestamptz published_at
    uuid published_by
  }
  plugin_permissions {
    uuid plugin_version_id FK
    text permission
    uuid approved_by
    timestamptz approved_at
  }
  plugin_subscriptions {
    uuid id PK
    text url
    text public_key
    timestamptz last_checked_at
  }
  plugin_drafts {
    uuid id PK
    uuid plugin_id FK
    jsonb files
    int version
  }
  plugin_storage {
    uuid plugin_id
    uuid channel_id
    text key
    bytea value
  }
```

## 3. 渠道、模型与路由（✅ 渠道/模型映射/价格；路由组在 Phase 3）

```mermaid
erDiagram
  channels ||--o{ channel_versions : "配置历史"
  channels ||--o{ channel_secrets : "加密字段"
  channels ||--o{ channel_shares : "shared 作用域"
  channels ||--o{ channel_models : ""
  models ||--o{ channel_models : "逻辑模型 → 渠道模型"
  models ||--o{ model_aliases : ""
  channel_models ||--o{ pricing_versions : "成本价"
  models ||--o{ pricing_versions : "售价"
  route_groups ||--o{ route_rules : ""
  route_groups ||--o{ route_versions : ""
  channels {
    uuid id PK
    uuid owner_id FK
    uuid workspace_id
    text scope "private|shared|global"
    uuid plugin_version_id FK "固定版本，不自动漂移"
    jsonb config "非敏感配置"
    int weight
    int priority
    int max_concurrency
    text health_state "healthy|degraded|half_open|open|disabled"
    int version
  }
  channel_secrets {
    uuid channel_id FK
    text field
    text ciphertext "v1:kid:..., AAD=channel_secret:id:field"
  }
  channel_shares {
    uuid channel_id FK
    text grantee_type "user|role"
    text grantee
  }
  models {
    uuid id PK
    text name "逻辑模型名"
    text scope
  }
  channel_models {
    uuid channel_id FK
    uuid model_id FK
    text upstream_model
    bool enabled
  }
  model_aliases {
    text alias
    uuid model_id FK
  }
  pricing_versions {
    uuid id PK
    text target_type "channel_model|model"
    uuid target_id
    text currency
    jsonb prices "每 1M token，按维度"
    timestamptz effective_at
  }
  route_groups {
    uuid id PK
    text strategy "fixed|priority|weighted|latency|cost"
    int version
  }
```

## 4. Gateway Key ✅

```mermaid
erDiagram
  users ||--o{ gateway_keys : ""
  gateway_keys ||--|| key_policies : ""
  gateway_keys ||--o{ budgets : ""
  gateway_keys {
    uuid id PK
    uuid user_id FK
    text name
    text prefix "og-xxxx 用于识别"
    bytea key_hash "SHA-256"
    text status
    timestamptz expires_at
    timestamptz revoked_at
    uuid rotated_from
  }
  key_policies {
    uuid key_id FK
    jsonb allowed_models
    jsonb allowed_channels
    jsonb ip_cidrs
    int rpm
    int tpm
    int max_concurrency
    jsonb features "stream/tools/image/audio/file"
    text compat_mode "error|warn"
  }
  budgets {
    uuid id PK
    uuid key_id FK
    text window "day|month"
    bigint limit_nano
  }
```

## 5. 计费、订阅与兑换（✅ 钱包/账本/预留/兑换码；套餐与订阅在 Phase 3）

```mermaid
erDiagram
  plans ||--o{ subscriptions : "快照 rules"
  users ||--o{ subscriptions : ""
  users ||--|| wallets : ""
  wallets ||--o{ ledger_entries : "只追加"
  wallets ||--o{ reservations : "预留"
  subscriptions ||--o{ quota_usage : "按规则与窗口"
  redeem_batches ||--o{ redeem_codes : ""
  redeem_codes ||--o{ redemptions : ""
  users ||--o{ redemptions : ""
  plans {
    uuid id PK
    text name
    jsonb rules "QuotaRule[]"
    jsonb models
    text duration
    bool stackable
    text status
    int version
    jsonb template_ref
  }
  subscriptions {
    uuid id PK
    uuid user_id FK
    uuid plan_id FK
    jsonb plan_snapshot
    timestamptz starts_at
    timestamptz ends_at
    text status
    text source "redeem|admin|payment"
    text source_ref
  }
  quota_usage {
    uuid subscription_id FK
    text rule_id
    timestamptz window_start
    numeric used "PK(subscription_id, rule_id, window_start)"
  }
  wallets {
    uuid id PK
    uuid user_id FK
    bigint balance_nano
    bigint reserved_nano
    int version
  }
  ledger_entries {
    uuid id PK
    uuid wallet_id FK
    text kind
    bigint amount_nano
    bigint balance_after_nano
    text ref_type
    text ref_id
    uuid created_by
    text note
  }
  reservations {
    text request_id PK
    uuid wallet_id FK
    bigint amount_nano
    timestamptz expires_at
  }
  redeem_batches {
    uuid id PK
    text kind "wallet_credit|plan|invite"
    jsonb payload
    int max_redemptions_per_code
    int per_user_limit
    timestamptz valid_from
    timestamptz expires_at
    text status
    uuid created_by
  }
  redeem_codes {
    uuid id PK
    uuid batch_id FK
    bytea code_hash "SHA-256, UNIQUE"
    text prefix
    int used_count
  }
  redemptions {
    uuid id PK
    uuid code_id FK
    uuid user_id FK
    text result_ref
    timestamptz created_at
  }
  fx_rates {
    text base
    text quote
    numeric rate
    timestamptz effective_at
  }
```

## 6. 观测与事实表（✅ `request_logs` 按月分区；健康事件、任务、通知在后续阶段）

```mermaid
erDiagram
  request_logs ||--o{ usage_facts : ""
  request_logs ||--o{ cost_facts : "每次上游尝试"
  request_logs ||--o| charge_facts : "每个请求至多一条"
  request_logs {
    text request_id PK
    uuid user_id
    uuid key_id
    text protocol
    text logical_model
    uuid channel_id
    text upstream_model
    int status
    text error_class
    int attempts
    jsonb fallback_path
    int ttft_ms
    int duration_ms
    timestamptz started_at
  }
  usage_facts {
    text request_id
    bigint input
    bigint output
    bigint cache_read
    bigint cache_write
    bigint reasoning
    bool estimated
    text plugin_version "custom 计量来源"
  }
  cost_facts {
    text request_id
    int attempt
    uuid channel_id
    uuid pricing_version_id
    text currency
    bigint amount_orig_nano
    bigint amount_nano
    uuid fx_rate_id
  }
  charge_facts {
    text request_id PK
    text basis "subscription|wallet"
    uuid subscription_id
    uuid pricing_version_id
    bigint amount_nano
    numeric multiplier
  }
  health_events {
    uuid channel_id
    text kind "passive|active"
    text state
    text detail
    timestamptz at
  }
  background_jobs {
    uuid id PK
    text kind
    text status
    jsonb progress
    timestamptz run_at
  }
  notifications {
    uuid id PK
    uuid user_id
    text kind
    jsonb payload
    timestamptz read_at
  }
```

- `request_logs` 及各事实表按月分区（`PARTITION BY RANGE (started_at)`）；统计看板读取小时/日聚合表，不直接扫明细表。
- 默认**不保存** Prompt、响应正文、Authorization 头、上游 Key、完整客户端 IP。
