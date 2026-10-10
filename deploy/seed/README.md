# 目录种子（catalog seed）

`omnigate seed` 把一份声明式的「目录」——**售价 / 成本价、模型资料、套餐**——写入数据库。它是 `omnigate` 二进制的子命令
（镜像是 distroless，没有 shell），走的是与管理后台相同的服务层：**校验规则、审计日志与后台操作完全一致**。

镜像（二进制）**内置**一份目录，源文件是 [`server/internal/seed/builtin/catalog.json`](../../server/internal/seed/builtin/catalog.json)
（`//go:embed`，唯一的数据来源）：

| 部分 | 内容 |
|---|---|
| `prices` | 8 个 GPT 模型的售价：`gpt-5.6-luna/sol/terra`、`gpt-6-astra/luna/sol`、`gpt-6.1-sol`（按百万 tokens；`gpt-6-astra` 与 `gpt-6.1-sol` 带超过 272K 输入 token 的长上下文阶梯价），`gpt-image-2`（按张，$0.05） |
| `modelInfo` | 上述 8 个模型的资料（上下文长度、能力标签等） |
| `plans` | 6 个套餐 Go → Elite（见文末「套餐介绍」） |

金额以 **USD** 计价。查看或导出：`docker run --rm ghcr.io/okysu/omnigate:latest seed export-builtin > catalog.json`（不需要数据库）。

- 只写入差异，可以反复执行（幂等）；`--dry-run` 只打印将要发生的变更，不写任何数据。
- **从不删除**：目录里没有的价格、模型资料、套餐保持原样。下架套餐请在后台操作（或在文件中把 `status` 改为 `archived`）。
- 不启动 HTTP 服务，可以在服务运行时执行（SQLite 也可以）；新价格在 10 秒内被网关使用，套餐与模型资料立即生效。

## 1. 首次启动自动导入（推荐）

`omnigate.env` 中设置：

```dotenv
OMNIGATE_SEED_ON_START=builtin
```

服务启动时按顺序：执行迁移 → 锁定结算币种（`OMNIGATE_CURRENCY`，首次启动写入）→ 导入目录 → 开始监听。导入成功后在
`system_settings` 写入标记 `seed.applied`（`{"source":"builtin","sha256":"…","appliedAt":"…","trigger":"startup","changes":22}`）：

| 情况 | 行为 |
|---|---|
| 新数据库 | 全部新建（日志 `catalog seeded on first start … summary="价格：新建 8；模型资料：新建 8；套餐：新建 6"`） |
| 已有相同数据但没有标记（例如之前手动导入过） | 不写任何价格 / 套餐，只写入标记（`written=0`） |
| 已有标记 | 跳过（`catalog seeding skipped: this database was already seeded once`）。后台改过的价格、套餐不会被覆盖 |
| 已有标记，但新镜像的内置目录变了 | 仍然跳过，日志提示 `the catalog has changed since` 与手动命令；**不会自动导入** |
| 结算币种不是 USD | ERROR 日志 `catalog currency differs from the settlement currency`，跳过，服务照常启动 |
| 目录无效 / 写入失败 | ERROR 日志，服务照常启动；没有写入标记，下次启动重试（幂等） |

取值：`builtin`（内置目录）、容器内的文件路径（如 `/data/catalog.json`，自定义目录）或留空（默认，不导入）。
手动执行 `omnigate seed …` 成功后同样会更新这个标记。

## 2. 手动导入

前提：服务已经启动过一次（数据库迁移已完成，结算币种已锁定）。内置目录以 USD 计价，生产的
`OMNIGATE_CURRENCY` 必须是 `USD`；文件里的 `"currency": "USD"` 与数据库中锁定的币种不一致时，命令会报错并且不写入任何数据。
命令在部署目录（`/opt/omnigate`，即仓库 `deploy/` 的副本）执行。

**内置目录**

```bash
docker compose exec omnigate omnigate seed builtin --dry-run   # 试运行：校验结果和变更清单
docker compose exec omnigate omnigate seed builtin             # 正式写入
```

**自定义目录文件**：先导出内置目录作为起点，修改后通过标准输入传入：

```bash
docker compose exec -T omnigate omnigate seed export-builtin > catalog.json   # 或 seed export 导出当前数据库
# 编辑 catalog.json …
docker compose exec -T omnigate omnigate seed - --dry-run < catalog.json
docker compose exec -T omnigate omnigate seed - < catalog.json
```

`-` 表示从标准输入读取；`-T` 关闭伪终端，否则重定向的内容传不进容器。也可以把文件复制进卷：
`docker compose cp catalog.json omnigate:/data/` 后执行 `docker compose exec omnigate omnigate seed /data/catalog.json`。

`seed` 读取的是容器里已有的环境变量（`OMNIGATE_DATABASE_URL` 等，与 `omnigate migrate` 相同），使用 PostgreSQL 时也一样。
不用 Compose 时：`docker exec omnigate omnigate seed builtin`、`docker exec -i omnigate omnigate seed - < catalog.json`。

### 输出与退出码

每个条目一行：`新建` / `新版本`（价格）/ `更新`（模型资料、套餐，附差异摘要）/ `不变`，最后是汇总行。例如再次执行同一份文件：

```text
汇总： 价格：不变 8；模型资料：不变 8；套餐：不变 6
无需变更：数据库已与目录一致。
```

- 文件有任何问题（JSON 语法、未知字段、类型错误、校验失败、渠道或套餐名有歧义、币种不一致）时，**一次列出全部问题**，
  每条带字段路径（如 `plans[2].rules[0].limit: 必须大于 0`），退出码非 0，**不写入任何数据**。
- 校验通过后逐条写入；万一中途出错（如数据库断开），已写入的部分会列出，修复后重新执行同一文件即可继续。

### 审计

每条写入都有审计日志：操作者为系统操作者 `seed`（`actorId` 为空），`metadata.source = "seed"`，同一次执行的所有条目共用一个
`requestId`（`seed-<uuid>`，执行结束时打印）。可在后台「审计日志」中查看。

## 3. 幂等规则

| 类别 | 匹配键 | 行为 |
|---|---|---|
| 价格 | `kind` + `model`（+ cost 价格的渠道） | 当前生效版本的各项金额与分时设置完全相同 → 跳过；否则**新增一个立即生效的版本**（价格版本只增不改，历史账单不受影响） |
| 模型资料 | `model` | 不存在 → 新建；内容不同 → 整条覆盖（与后台「保存」相同） |
| 套餐 | `name`（精确匹配，含已下架的套餐） | 不存在 → 新建；有差异 → 原地更新并打印差异（价格、规则的增删改等）。**已有订阅保留开通时的快照，不受影响**，新开通与续期之后才按新规则 |

其他说明：

- 新建套餐按文件中的顺序展示：用户的套餐目录按「最新在前」排列，`seed` 会从最后一个开始创建，所以文件里的第一个套餐显示在最前面。
- 数据库里有多个同名套餐、或 cost 价格引用的渠道名不唯一时会报错，请先在后台改名。
- 某个价格已经有「将来生效」的版本时会给出警告：到期后那个版本仍会取代本次写入的价格。
- 通过 `seed` 修改售价**不会**发送「模型价格调整」通知（后台改价会通知最近 30 天调用过该模型的用户）；对已有用户调价时请另行告知，或在后台修改。

## 4. 文件格式

```jsonc
{
  "currency": "USD",            // 可选：金额的币种，必须与目标部署一致（不会换算）
  "prices": [ … ],              // 可选
  "modelInfo": [ … ],           // 可选
  "plans": [ … ]                // 可选；缺少的部分不做任何处理
}
```

解析是严格的：未知字段、类型错误（例如金额写成数字而不是字符串）都会报错。金额一律是**十进制字符串**，最多 9 位小数。

### `prices[]`（与 `POST /api/admin/prices` 相同）

```json
{ "kind": "sell", "model": "gpt-6-sol",
  "inputPerM": "2", "outputPerM": "10", "cacheReadPerM": "0.2", "cacheWritePerM": "2.5",
  "perRequest": "0", "perImage": "0",
  "imageInputPerM": null, "audioInputPerM": null, "audioOutputPerM": null,
  "perMinute": "0", "perMCharacters": "0",
  "schedule": null, "scheduleTimezone": "Asia/Shanghai",
  "tiers": null,
  "channelName": null }
```

| 字段 | 说明 |
|---|---|
| `kind` | `sell`（售价，按逻辑模型）或 `cost`（成本价，按渠道 + 上游模型）；省略为 `sell` |
| `model` | 模型名（1–128 字符） |
| `inputPerM` / `outputPerM` / `cacheReadPerM` / `cacheWritePerM` | 每百万 tokens 的价格；省略或 `""` 为 0 |
| `perRequest` / `perImage` | 每次请求 / 每张输出图片；省略为 0 |
| `imageInputPerM` / `audioInputPerM` / `audioOutputPerM` | 图片输入、音频输入/输出每百万 tokens；`null` 表示按 `inputPerM` / `outputPerM` 计费 |
| `perMinute` / `perMCharacters` | 每分钟输入音频（按秒折算）/ 每百万语音合成字符；省略为 0 |
| `schedule` | 分时倍率时段，与后台相同：`[{"days": [1,2,3,4,5], "start": "00:00", "end": "08:00", "multiplier": "0.5"}]`；`null` 为不分时 |
| `scheduleTimezone` | 分时时区（IANA 名称），省略为 `Asia/Shanghai` |
| `tiers` | 按上下文长度的阶梯价格（最多 5 档），与后台相同：`[{"aboveInputTokens": 272000, "inputPerM": "20", "outputPerM": "75", "cacheReadPerM": "2", "cacheWritePerM": null}]`；提示 token 数（输入 + 缓存）超过门槛时整次请求按该档计价，档内 `null` 或省略的单价沿用基础价格；`null` 或省略为不分档（见 `docs/contracts/phase10-api.md` §1） |
| `channelName` | **仅 cost 价格**：渠道名称（导入时按名称查找渠道，因为两个环境的渠道 ID 不同）；sell 价格必须为 `null` |

### `modelInfo[]`（与 `PUT /api/admin/model-info/{model}` 相同）

```json
{ "model": "gpt-6-sol", "displayName": "", "description": "", "vendor": "OpenAI", "tags": [],
  "contextWindow": 1000000, "maxOutput": 128000,
  "capabilities": { "vision": true, "tools": true, "reasoning": true, "embedding": false,
                    "imageGeneration": false, "audioInput": false, "audioOutput": false },
  "hidden": false, "sortOrder": 0 }
```

限制与后台一致：`displayName` ≤ 100 字、`description` ≤ 1000 字、`vendor` ≤ 50 字、最多 10 个标签（每个 ≤ 20 字）、
`maxOutput` 不能超过 `contextWindow`、`sortOrder` 为 ±999999999 内的整数。省略的字段取默认值（空 / `false` / 0），整条覆盖。

### `plans[]`（与 `POST /api/admin/billing/plans` 相同）

```json
{ "name": "Go 启航者", "description": "…", "listPrice": "3", "duration": "30d",
  "models": [], "stackable": false, "status": "active",
  "rules": [
    { "id": "5h",      "label": "5 小时会话限额", "meter": "charge", "window": { "kind": "session", "duration": "5h" }, "limit": "6" },
    { "id": "weekly",  "label": "每周会话限额",   "meter": "charge", "window": { "kind": "session", "duration": "7d" }, "limit": "12" },
    { "id": "monthly", "label": "月度总额度",     "meter": "charge", "window": { "kind": "period",  "every": "30d" },   "limit": "24" }
  ] }
```

| 字段 | 说明 |
|---|---|
| `name` | 1–100 字，**匹配键** |
| `description` | ≤ 2000 字，展示在用户的套餐目录中（保留换行） |
| `listPrice` | 标价（十进制字符串），`null` 为未标价 |
| `duration` | 每份有效期，`<数字><m/h/d>`，1h–366d |
| `models` | 覆盖的模型，`[]` 为全部模型 |
| `stackable` | `false`：重复开通 = 续期同一订阅；`true`：每次开通一份独立订阅 |
| `status` | `active`（默认）或 `archived` |
| `rules` | 1–10 条规则，格式与后台 API 完全相同：`id`（小写字母、数字、`-`、`_`，≤ 32，套餐内唯一）、`label`（≤ 64 字）、`meter`（`requests`、`tokens.input`、`tokens.output`、`tokens.total`、`images`、`audio_seconds`、`charge` 或 `custom:<插件>.<计量>`）、`window`（`calendar` + `unit`/`timezone`，`rolling`/`session` + `duration`（5m–31d），`period` + `every`（1h–366d），`lifetime`）、`limit`（十进制字符串，> 0；除 `charge` 与插件计量外必须为整数）、可选 `models`、`modelWeights` |

`charge` 计量按「模型售价 × 用户组倍率」累计金额；本目录的套餐用户组倍率为 1，即按官方原价计算。

## 5. 从开发环境导出 / 更新内置目录

```bash
cd server
set -a && source ../.env.dev && source ../.env.dev.local && set +a
go run ./cmd/omnigate seed export > /tmp/catalog.json                                 # 全部：价格、模型资料、上架中的套餐
go run ./cmd/omnigate seed export --prices --model-info --no-cost > /tmp/catalog.json  # 只要售价和模型资料
```

- `--prices` / `--model-info` / `--plans` 选择要导出的部分（都不写 = 全部）；`--no-cost` 不导出成本价。
- 导出的是**当前生效**的价格版本、全部模型资料和**上架中**的套餐（按目录展示顺序），并带上 `currency`。
- 成本价用 `channelName` 引用渠道。生产环境的渠道名与开发环境不同时，导入会报「渠道不存在」——要么先在生产建同名渠道，
  要么用 `--no-cost` 只导出售价。内置目录不含成本价。
- 导出**只读**：PostgreSQL 连接以 `default_transaction_read_only=on` 打开，SQLite 以 `query_only` 打开（文件不存在时直接报错，不会新建），
  不执行迁移。数据库结构不是最新时会提示先运行 `omnigate migrate`。
- 生产环境同样可以导出（例如备份目录或带回开发环境）：`docker compose exec -T omnigate omnigate seed export > catalog.json`。

修改内置目录：编辑 `server/internal/seed/builtin/catalog.json`（只替换导出的 `prices` / `modelInfo` 两段，`plans` 直接编辑），
`go test ./internal/seed/` 会校验它（币种、条目数量）。新镜像发布后，已有数据库不会自动套用新目录（见第 1 节），
由管理员执行 `omnigate seed builtin --dry-run` 查看差异后手动导入。

## 6. 套餐介绍

以下内容与内置目录中的套餐一致，可直接用作宣传文案。

| 套餐名称 | 价格 | 5 小时会话限额 | 每周会话限额（7 天） | 月度总额度 | 适合谁 |
|---|---|---|---|---|---|
| Go 启航者 | $3 / 月 | $6 | $12 | $24 | 轻量体验、偶尔调用 |
| Plus 进阶者 | $10 / 月 | $20 | $40 | $80 | 个人日常开发与写作 |
| Pro 专业者 | $15 / 月 | $30 | $60 | $120 | 高频使用的专业开发者 |
| Max 高能者 | $20 / 月 | $40 | $80 | $160 | 重度使用、长时间编码会话（如 Claude Code） |
| Ultra 卓越者 | $30 / 月 | $60 | $120 | $240 | 小团队或多项目并行 |
| Elite 精英者 | $50 / 月 | $100 | $200 | $400 | 高强度、大规模调用 |

所有套餐的月度总额度都是售价的 **8 倍**，覆盖全部模型。

**套餐规则**

1. **三重限额并行生效**：5 小时会话、7 天会话、月度（按购买日起算的 30 天）同时管控，任意一项触达上限即限流——会话窗口从窗口内第一次调用开始计时，到期后整体刷新，月度额度在下一个订阅月开始时重置。
2. **Token 按官方原价计费**：模型消耗按官方原生定价计算，平台不加收额外费用。
3. **月度额度当月有效**：未使用额度不结转至下月。

### 规则如何对应到配置

| 限额 | 规则 | 含义 |
|---|---|---|
| 5 小时会话限额 | `session 5h` | 窗口内第一次调用时开启一个 5 小时窗口，窗口内的用量累计计算；到期后整体刷新，下一次调用再开启新窗口 |
| 每周会话限额 | `session 7d` | 同上，窗口长度为 7×24 小时；从用户自己的第一次调用开始计时，不按自然周、也不按开通日重置。重置卡或管理员重置会从重置时刻重新开启窗口 |
| 月度总额度 | `period 30d` | 从开通时刻起每 30 天为一个订阅月，每个订阅月独立计算、到期清零 |

- 套餐有效期 `duration: "30d"` 与月度窗口等长：**每买一个月，恰好对应一个独立的月度额度**，与在哪一天购买无关，
  这就是「当月有效、不结转」——不用自然月（`calendar month`），是因为月中购买的用户在自然月下只能用到半个月的额度。
- 套餐不可叠加（`stackable: false`）：再次开通同一套餐会把现有订阅顺延 30 天，月度窗口继续按首次开通时刻每 30 天重置，
  上一个订阅月剩余的额度不会带入下一个月。
- 计量为 `charge`：每次请求按模型售价折算金额后计入三条规则；售价即官方价格，用户组倍率为 1。
- 额度用尽后的行为由用户的「套餐额度用完后」设置决定：默认阻断请求（HTTP 429）直至窗口恢复，也可改为使用钱包余额按量计费。
