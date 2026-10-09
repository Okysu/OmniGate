# ADR-0007：金额定点表示（nano 单位 int64）

- 状态：已接受
- 日期：2026-10-08

## 背景

需求禁止用浮点数表示金额。按 token 计价时单价极小（例如 0.15 USD / 1M token，即每 token 1.5×10⁻⁷ USD），
以“分”为单位会丢失精度。

## 决策

- 金额类型 `money.Amount` = `int64` 个 **10⁻⁹ 结算币种单位**（nano）。可表示 ±92 亿个货币单位，
  对单个部署的账本足够；单个请求的成本精确到 10⁻⁹。
- 单价统一以 **“每 100 万 token 的价格”** 保存（`Amount`），成本 = `单价 × token 数 / 1e6`，
  用 `math/big` 精确计算后**四舍五入（远离零）**到 nano。
- 数据库列类型：单条金额用 `bigint`；跨大量行的汇总用 PostgreSQL `NUMERIC` 计算，避免 int64 溢出。
- API 中金额以**十进制字符串**传输（如 `"12.345"`），前端按 `/api/system/info` 返回的币种小数位数显示；
  前端不做金额运算。
- 汇率用 `NUMERIC(20,10)` 保存，换算时同样用 big 运算并四舍五入到 nano。

## 实现

`server/internal/money`：`Parse`、`String`、`Format(decimals)`、`Round`、`Add`（溢出检查）、`MulDiv`、`TokenCost`，
附带单元测试（四舍五入、溢出、MinInt64 边界）。
