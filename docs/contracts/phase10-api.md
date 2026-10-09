# 契约：Round 10 —— 按上下文长度的阶梯计费、分时价格浮层

> 状态：**已实现**。通用约定同前；所有 SQL 同时支持 PostgreSQL 与 SQLite（迁移 `00017_price_tiers`）。
> 需求原文：“完善阶梯计费，因为 token 可能会按照上下文大小计费”；以及“分时角标的提示太难读”。

## 1. 按上下文长度的阶梯价格

部分上游按**整次请求的上下文长度**分档计价：提示超过某个长度（OpenAI 为 272K 输入 token）后，这次请求的**全部** token 都按长上下文单价计费，
而不是只有超出的部分。例如：

| 模型 | ≤272K：输入 / 缓存读取 / 缓存写入 / 输出 | >272K：输入 / 缓存读取 / 缓存写入 / 输出 |
| --- | --- | --- |
| gpt-6-astra | 10 / 1 / 12.5 / 50 | 20 / 2 / 25 / 75 |
| gpt-6.1-sol | 2 / 0.10 / 2.5 / 10 | 4 / 0.20 / 5 / 15 |
| gpt-5.5 | 5 / 0.50 / – / 30 | 10 / 1 / – / 45 |
| gpt-5.5-pro | 30 / – / – / 180 | 60 / – / – / 270 |

### 1.1 数据模型

价格版本（售价与成本价）新增可选字段 `tiers`（null = 不分档）：

```json
"tiers": [
  { "aboveInputTokens": 272000, "inputPerM": "20", "outputPerM": "75",
    "cacheReadPerM": "2", "cacheWritePerM": "25",
    "imageInputPerM": null, "audioInputPerM": null, "audioOutputPerM": null }
]
```

| 字段 | 说明 |
| --- | --- |
| `aboveInputTokens` | 门槛：请求的提示 token 数**大于**它时使用本档。1–1 000 000 000 的整数，各档严格递增 |
| `inputPerM`、`outputPerM` | 本档的输入、输出单价，必填 |
| `cacheReadPerM`、`cacheWritePerM`、`imageInputPerM`、`audioInputPerM`、`audioOutputPerM` | 可选；省略、null 或空字符串 = **沿用基础价格的同名字段**（原样继承：基础价格的 `imageInputPerM` 等本身为 null 时，仍按本档的 `inputPerM` / `outputPerM` 计） |

- 最多 5 档，按 `aboveInputTokens` 升序保存。
- `perRequest`、`perImage`、`perMinute`、`perMCharacters` 不分档。
- 单价的格式与基础价格相同（非负十进制字符串，最多 9 位小数）。

### 1.2 计价

- **提示 token 数** = 输入 + 缓存读取 + 缓存写入 token（`usage.input` 不含缓存 token；图片、音频输入 token 是 `input` 的一部分）。
  这与上游衡量上下文长度的口径一致：缓存命中的长提示同样是长上下文。输出 token（含推理）不计入。
- 选档：取 `aboveInputTokens` **小于**提示 token 数的最高一档；没有则用基础价格。**等于门槛时仍是较低一档**（272 000 按基础价，272 001 按长上下文价）。
- 选中的档为这次请求的**所有** token 部分定价（文本输入、图片输入、音频输入、输出、音频输出、缓存读取、缓存写入）；固定费用照常加上。
- 分时倍率与用户组倍率照常作用在总额上，仍只四舍五入一次：`金额 = round(基础金额(选中档的单价) × 分时倍率 × 用户组倍率)`。
- 成本价同样可以分档，用同一个提示 token 数选档（成本价不乘用户组倍率）。
- **预扣**（`admit`）：用估算的提示 token 数（请求体字节数 / 4，图片接口为提示文本，音频接口见 phase9）选档，预扣额 = 该档估算金额（仍受余额与限额封顶）。
- **结算**：用上游报告的实际用量选档。套餐覆盖的请求，`quotaCharge`（`charge` 计量）同样按选中的档计算。

### 1.3 API

| 接口 | 变化 |
| --- | --- |
| `POST /api/admin/prices` | 请求体新增可选 `tiers`（省略、null 或 `[]` = 无）。校验失败 → `422`：档数超过 5 时 `details.tiers`；字段错误为 `details["tiers[i].<字段>"]`，如 `tiers[1].aboveInputTokens`（不递增或越界）、`tiers[0].inputPerM`（缺失或格式错误）。档内的未知字段与其他未知字段一样被拒绝（`422`，请求体不合法） |
| `GET /api/admin/prices`、`POST` 的响应、`GET /api/models` 的 `price` | 新增 `tiers`：原样返回保存的档（可选单价 null = 沿用基础价格）；没有时为 null |
| `GET /api/plaza/models`、`GET /api/plaza/mine`（`price` 与 `basePrice`） | `PlazaPrice` 新增 `tiers`：**已展开继承**并乘以查看者的用户组倍率（与基础单价相同）。`cacheReadPerM` / `cacheWritePerM` 为 0 时为 null；`imageInputPerM`、`audioInputPerM`、`audioOutputPerM` 为 null 表示按本档的 `inputPerM` / `outputPerM` 计 |
| `GET /api/logs`（请求日志列表） | 请求日志新增 `priceTier`：售价命中的档的 `aboveInputTokens`；null = 基础价格、未定价或升级前的日志 |

价格版本的审计元数据（`price.create`）带 `tiers`；售价变化通知（`model.price_changed`）的正文列出各档的输入 / 输出单价，
只改了档位也算价格变化。

### 1.4 种子目录

`omnigate seed` 的价格条目新增 `tiers`，格式与 API 相同（`null` 或省略 = 不分档；导出时没有档位写为 `null`，档内继承的单价写为 `null`）。
不带 `tiers` 的旧目录仍然有效。幂等比较包含档位：档位不同（包括把继承的单价显式写成与基础价相同的值）会产生新版本。
档位错误按路径报告，如 `prices[3].tiers[0].outputPerM`。

内置目录（`omnigate seed builtin`）为 `gpt-6-astra` 与 `gpt-6.1-sol` 加了 >272K 的长上下文档（上表的价格）。

### 1.5 存储

迁移 `00017_price_tiers`（PostgreSQL 与 SQLite，都有 Down）：`prices.tiers`（jsonb / 带 `json_valid` 检查的 TEXT，NULL = 无），
`request_logs.price_tier`（bigint / INTEGER，NULL = 基础价格）。档位以 JSON 保存，单价为十进制字符串（与 `schedule` 相同）。

## 2. 控制台

- **价格对话框**：按 Token 与自定义组合两种计费方式下提供“阶梯价格（按上下文长度）”：每行一个门槛（可输入 `272K`、`1M` 或整数）和该档的 token 单价，
  最多 5 档，门槛递增校验；预设按钮“长上下文：>272K 输入 token 全部单价翻倍”按基础价预填 2× 输入 / 缓存读取 / 缓存写入、1.5× 输出；
  下方一行预览，如“≤272K：输入 $10 · 输出 $50；>272K：输入 $20 · 输出 $75”。其他计费方式不提交 `tiers`。
- **价格表、模型广场卡片 / 详情、我的模型**：有档位的价格显示“阶梯”角标，悬停 / 聚焦 / 点按弹出各档单价；广场详情的价格表列出所有档，
  费用计算器按填写的输入（含缓存）token 数选档。
- **请求日志**：命中档位的请求显示“长上下文档 >272K”角标。
- **分时角标**（§2 需求）：改为与阶梯角标相同的浮层：标题“分时价格 · 时区”，每个时段一行（日期合并为“周一至周五”“周六、周日”“每天”，
  时间段不换行，倍率显示为“5 折” / “×1.5”并按列对齐），页脚“其余时段按原价”。鼠标悬停、键盘聚焦、手机点按都能打开，Esc 关闭。
- 金额仍按 3 位小数显示规则。

## 3. 实现差异

实现位置：`server/internal/pricing/tiers.go`（数据模型、校验、选档、继承）、`pricing.go`（`Compute`、存储、API）、
`server/internal/gateway/gateway.go`（预扣与结算、`priceTier`）、`server/internal/plaza/plaza.go`（展开的广场档位）、
`server/internal/requestlog`、`server/internal/seed`；前端见 `web/`。

| # | 条款 | 说明 | 原因 |
| --- | --- | --- | --- |
| 1 | §1.2 提示 token 数 | 明确为 输入 + 缓存读取 + 缓存写入；输出与推理 token 不计入；等于门槛用较低一档 | 与 OpenAI / Gemini 长上下文计价的口径一致（缓存命中的 token 也是上下文） |
| 2 | §1.1 继承 | 档内省略的可选单价**原样**继承基础价格的字段（含 null）：基础价格没有单独的图片 / 音频 token 单价时，长上下文档的图片 / 音频 token 跟随本档的输入 / 输出单价 | 只填输入 / 输出就能得到“整档翻倍”的效果，不会意外按短上下文价计图片 / 音频 token |
| 3 | §1.3 API 形状 | 管理接口返回保存的原始档位（null = 继承），广场返回展开后的档位 | 管理端需要能原样编辑和复制版本；广场只用于展示与估算 |
| 4 | §1.3 `priceTier` | 只记录**售价**命中的档；成本价按自己的档位计价，但不单独记录 | 日志里的金额（`charge` / `quotaCharge`）来自售价；成本价可从成本价版本推出 |
| 5 | §1.2 路由 | `lowest_cost`（最低成本）路由策略比较渠道成本时仍用成本价的基础输入 + 输出单价（乘分时倍率），不考虑档位 | 路由发生在知道实际用量之前；按基础价排序与此前一致 |
| 6 | §1.2 预扣 | 估算的提示 token 数按请求体字节数 / 4 计算（含 JSON 结构），接近门槛的请求可能按长上下文价预扣、按基础价结算 | 预扣只是冻结额度，结算时以实际用量为准并释放差额 |
| 7 | §1.1 适用范围 | 服务端任何计费方式都接受 `tiers`；控制台只在按 Token 与自定义组合方式下提供编辑 | 按次 / 按张 / 按时长没有 token 单价可分档 |
| 8 | §1.4 内置目录 | 内置目录的 `gpt-6-astra`、`gpt-6.1-sol` 新增长上下文档；启动时的自动种子每个数据库只运行一次，已部署的实例需要手动执行 `omnigate seed builtin`（会为这两个模型各新增一个价格版本） | 不在升级时静默改价 |
