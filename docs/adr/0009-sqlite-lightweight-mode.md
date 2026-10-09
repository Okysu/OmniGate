# ADR-0009：SQLite 轻量模式

- 状态：已接受并实施（用户要求“最好也可以支持 SQLite 轻量化”）
- 日期：2026-10-08
- 决策人：用户、架构

## 背景

OmniGate 目前只支持 PostgreSQL（ADR-0001）。个人和小团队希望 `docker run` 一个容器就能用，不想再维护一个数据库实例。
配合单镜像、单端口部署，需要一个零依赖的嵌入式数据库选项。

当前后端约 190 处 SQL，用到的 PostgreSQL 特性有：
- `jsonb` 与 `jsonb_array_elements` 等 JSON 函数；
- `SELECT … FOR UPDATE` 行锁；
- `ON CONFLICT`、`RETURNING`；
- `= ANY($1)` 数组参数、`unnest`；
- `interval` 运算、`date_trunc`；
- `numeric(38,9)`（配额用量）；
- 请求日志按月分区（`PARTITION BY RANGE`）；
- pgx 的类型映射（jsonb 直接扫描进结构体，`uuid`、`timestamptz`）。

## 可选方案

1. **PostgreSQL 以外再加 SQLite，存储层通过方言适配**（本 ADR 选择）。
2. 镜像内嵌 PostgreSQL 二进制（embedded-postgres）：改动小，但镜像大几十 MB、进程多，不是真正的轻量。
3. 只支持 SQLite：放弃多实例与高并发写入，不可接受。

## 决策

1. **驱动**：`modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0` 下可用，单静态二进制、distroless 镜像都不受影响）。
2. **选择方式**：`OMNIGATE_DATABASE_URL` 以 `sqlite:` 开头（如 `sqlite:///data/omnigate.db`）时使用 SQLite，
   否则为 PostgreSQL。单镜像不设置数据库地址时默认 `sqlite:///data/omnigate.db`。
3. **数据访问层**（`internal/platform/db`）：定义自有接口 `db.Querier` / `db.Tx` / `db.Rows` / `db.Row` / `db.Result`，
   API 形状与当前用到的 pgx 子集一致（`Exec`、`Query`、`QueryRow`、`InTx`、`ErrNoRows`、唯一键与外键冲突判断），两个实现：
   - **PostgreSQL**：包装 pgxpool，行为不变。
   - **SQLite**：包装 `database/sql`。参数占位符 `$N` 改写为 `?N`；扫描时模拟 pgx 的语义：JSON 文本扫描进结构体、切片、map，
     UUID 和时间从文本解析。
   存储代码只把 `pgx.*` 类型换成 `db.*`，SQL 尽量写成两种数据库都支持的子集。
4. **方言差异**：通过 `db.Dialect` 提供少量辅助函数，存储代码不直接写数据库特有语法：
   - 时间运算：在 Go 中计算后作为参数传入（不用 `interval`）；
   - 时间截断：统计用的 `date_trunc` 对应 SQLite 的 `strftime`；
   - 集合参数：`= ANY($1)` 对应 SQLite 的 `IN (SELECT value FROM json_each(?1))`，参数传 JSON 数组；
   - JSON 函数：`jsonb_array_elements` 对应 `json_each`；
   - 行锁：SQLite 下去掉 `FOR UPDATE`，事务一律以 `BEGIN IMMEDIATE` 开始（DSN 参数 `_txlock=immediate`），
     效果等同于整库写锁，正确性不受影响。
5. **迁移**：`migrations/postgres/`（现有文件迁入）和 `migrations/sqlite/` 两套，版本号一一对应，由 goose 分别执行。
   SQLite 版本的类型对应关系：
   - `uuid`、`timestamptz` 存为 TEXT（RFC 3339，UTC，纳秒精度，字符串比较即时间比较）；
   - `jsonb` 存为 TEXT，并加 `CHECK(json_valid(...))`；
   - `numeric(38,9)` 存为 TEXT，加减法改到 Go 中（`subscription.Decimal`）在事务内完成；
   - 请求日志是单表，没有分区，按保留天数定期删除。
6. **运行参数**：WAL 模式、`busy_timeout=5000`、`foreign_keys=ON`、`synchronous=NORMAL`；连接池较小（读写共用，事务串行）。
7. **测试**：集成测试通过环境变量在两种数据库上都运行。CI 上 PostgreSQL 与 SQLite 各跑一遍，SQLite 用临时文件，不需要外部服务。

## 影响与代价

- **定位**：SQLite 模式只适合**单实例**、中低写入量（个人、小团队、内网工具）。写入串行：请求日志批量写，钱包、配额更新的事务都很短，
  预计可以支撑每秒数百个网关请求；更高负载或多实例部署请使用 PostgreSQL。
- **限制**：
  - 不支持多个实例共享同一个 SQLite 文件；
  - 不支持从 SQLite 在线迁移到 PostgreSQL；停机迁移用 `omnigate migrate-db`（2026-10-09 实施，见下文）。
- **维护成本**：两套迁移；新增存储代码必须同时在两种数据库上测试通过（CI 强制）。
- **备份**：SQLite 用 `VACUUM INTO` 或在线备份 API（后续提供 `omnigate backup` 子命令）。

## 实施记录（2026-10-08）

按上文决策实现，与设计的出入和细节：

- **数据访问层** `server/internal/platform/db`：`DB`（`Dialect()`、`Close()`、`Begin`、`CopyFrom`、`Ping`）与接口
  `Querier` / `Tx` / `Rows` / `Row` / `Result`，`InTx`、`CollectRows`、`ErrNoRows`（= `sql.ErrNoRows`，pgx 的错误也匹配）、
  `IsUniqueViolation`（含主键冲突）、`IsForeignKeyViolation`。PostgreSQL 实现直接透传 pgxpool（行为不变）。
- **SQL 改写**：存储代码仍写 PostgreSQL 语法，SQLite 实现在执行前改写一次并缓存：`$N` → `?N`、`= ANY($N)` →
  `IN (SELECT value FROM json_each(?N))`、去掉 `::type` 与 `FOR UPDATE/SHARE [OF t]`、`now()` → 规范格式的 UTC 时间、
  `GREATEST/LEAST` → `max/min`、`jsonb_array_length` → `json_array_length`；字符串字面量、引号标识符与注释不改。
  其余差异由 `Dialect` 辅助函数提供：`Percentile`（SQLite 上是自定义聚合 `og_percentile_cont`，与 `percentile_cont` 同为线性插值）、
  `SumText`（自定义聚合 `og_sum_text`，任意精度求和，替代 `sum()::text`，避免 int64 溢出）、`DateUTC`（`strftime`）、
  `JSONArrayElements`（`json_each` / `jsonb_array_elements … AS r(value)`）。只有配额用量读写（`unnest` 批量查询、numeric 加法）、
  advisory lock 与请求日志分区按方言分支。原本的 `interval` 运算全部改为 Go 计算后传参；`WITH … DELETE … RETURNING` 改为
  `DELETE … RETURNING` 后在 Go 中求和；`DELETE … USING` 改为 `EXISTS` 子查询；`LIKE` 显式写 `ESCAPE '\'`。
- **参数与扫描**：时间写为固定 9 位小数的 UTC RFC 3339 文本；`uuid` 等 `driver.Valuer` 取其值；切片、map、结构体编码为 JSON 文本；
  `[]byte` 是合法 JSON 时按文本（jsonb 列）存，否则按 BLOB（bytea 列）存。扫描时 JSON 文本可解码进结构体/切片/map，
  整数→bool，文本→时间（返回 UTC），`*T` 遇 NULL 置 nil，`sql.Scanner` 类型（uuid）照常工作。
- **写入串行化**：除 `_txlock=immediate` 与 `busy_timeout=5000` 外，进程内还有一把写锁：事务在**取连接之前**排队，
  非事务的写语句（`INSERT/UPDATE/DELETE…`）也排队。这样写事务不会占着连接等锁，读连接（WAL）不受影响，也不会出现
  `database is locked`。排队超过 30 秒报错。连接池 `max(4, GOMAXPROCS)`。
- **迁移**：`migrations/postgres/`（原文件原样移入）与 `migrations/sqlite/`（00001–00008，均有 Down），表为 `STRICT`。
  goose 只记录版本号，移动文件不影响已有数据库（已用 v8 的 PostgreSQL 库验证：新二进制 `migrate status` 为 `pending 0`，`migrate` 无操作）。
  SQLite 不能修改 CHECK 约束，00006、00008 采用官方推荐的重建表方式；迁移使用一条外键关闭的专用连接，结束后执行
  `PRAGMA foreign_key_check`。`omnigate migrate status` 与 `/readyz` 输出增加 `dialect` 字段。
- **配置**：Docker 镜像设置 `OMNIGATE_DATABASE_URL=sqlite:///data/omnigate.db`（容器内不设置即为 SQLite）；
  进程本身仍要求显式设置（开发环境由 `.env.dev` 指向 PostgreSQL）。SQLite 文件所在目录不存在时自动创建。
- **备份**：已提供 `omnigate backup <文件>`（`VACUUM INTO`，运行中可用，仅 SQLite）。
- **测试**：`internal/platform/db/dbtest` 根据 `OMNIGATE_TEST_DATABASE_URL`（`postgres://…` 或 `sqlite`）为每个用例提供独立数据库；
  全部集成测试在两种数据库上运行（`make test-integration`、`make test-sqlite`，CI 各跑一遍）。另有数据访问层测试（改写、扫描、
  约束错误、时间排序、并发写入、聚合函数在两种数据库上结果一致、迁移 Down/Up）以及钱包预留与配额记录的并发测试。
- **容量参考**（开发机，假上游，计费开启，单用户 32 并发，非流式）：SQLite 约 2,400 请求/秒（p99 约 24 ms），同机 Docker 中的
  PostgreSQL 约 320 请求/秒（单个钱包行锁 + 每条语句一次网络往返；`synchronous_commit=off` 时约 490）。多用户、多实例场景
  PostgreSQL 可以水平扩展，SQLite 不能。

## 实施记录：数据迁移工具（2026-10-09）

`omnigate migrate-db --from <url> --to <url> [--batch 1000] [--dry-run] [--force-empty-check=false]`
（契约 [phase8-api.md §4](../contracts/phase8-api.md)，代码 `server/internal/platform/db/transfer`，运维步骤见
[deployment.md 第 9 节](../operations/deployment.md#9-sqlite-迁移到-postgresql以及反向)）：

- **双向、不写死表**：表、列、主键与外键从目标库结构读取（PostgreSQL 用 `pg_catalog`，SQLite 用 `sqlite_master`、
  `pragma_table_xinfo`、`pragma_foreign_key_list`），排除 goose 的版本表与分区子表；新增迁移（新表、新列）无需修改工具。
  两边的表与列名必须完全一致（同一版本），否则拒绝。唯一的特例是请求日志的月分区：写入 PostgreSQL 前对源数据涉及的每个月调用
  `requestlog.EnsurePartitions`，避免数据落入 DEFAULT 分区。
- **类型转换按列类型**（以 PostgreSQL 一侧的类型为准）：`uuid` ↔ 文本、`timestamptz` ↔ 固定 9 位小数的 UTC 文本、`jsonb` ↔ JSON 文本、
  `numeric` ↔ 十进制文本（写入 SQLite 时去掉末尾的 0，与应用写法一致）、`bytea` ↔ BLOB、`boolean` ↔ 0/1，文本/整数数组 ↔ JSON 数组；
  枚举、区间等不支持的类型在开始前报错。SQLite → SQLite 原样复制存储值。
- **顺序与环**：按外键拓扑排序。SQLite 目标在复制连接上关闭外键检查，提交前 `PRAGMA foreign_key_check`；PostgreSQL 目标在一个事务中
  `COPY`，有 DEFERRABLE 约束时 `SET CONSTRAINTS ALL DEFERRED`，不可延迟的自引用或环通过可空外键列打断（先写 NULL、全部表复制后按主键回填），
  NOT NULL 且不可延迟的环拒绝。复制后重置 identity / serial 序列（当前 schema 没有）并 `ANALYZE`。
- **版本与空库**：任意一边结构版本高于二进制则拒绝；落后的一边先迁移到最新版本（已是最新的源库不会被写入）。目标库除迁移自带的初始行
  （用一个临时迁移出的 SQLite 库确定每表的初始行数，例如 `billing.enforce` 设置、默认用户组；按行数比较，迁移用生成的 id 播种也不受影响）外必须为空；`--force-empty-check=false` 时在复制事务内清空目标表再写入。
- **一致性**：源库在一个只读快照中读取（PostgreSQL：`REPEATABLE READ READ ONLY`，会话 `default_transaction_read_only=on`；
  SQLite：`mode=ro`、`query_only` 连接上的读事务），目标库一个事务写入，失败整体回滚。校验在同一快照的源数据与提交后的目标之间比较
  每表行数和所有 `*_nano` 金额列合计。`--dry-run` 不迁移、不写入（目标 SQLite 文件不存在时也不创建），只报告版本、待执行迁移、
  源库各表行数与目标是否为空。
- **测试**：合成 schema（全部类型、空 BLOB、NULL、环、自引用、可延迟约束、分区表、serial）在四个方向上复制并逐行比对；
  端到端测试用真实应用流程造数（登录、加密渠道凭据、Key、价格、钱包与兑换、套餐与配额、跨两个月的请求日志、带存储的插件、通知、
  SMTP 密码），SQLite → PostgreSQL → SQLite 往返迁移，每一步逐表比对数据，并在目标上启动应用通过 API 验证（已有会话、重新登录、
  网关请求带上正确的上游密钥、余额与流水、日志与统计、订阅用量继续累计、插件存储延续）；另有非空目标、版本过新、试运行不改动的用例。
- **限制**：需要停机（按快照复制，快照之后的写入不会被复制）；SQLite → PostgreSQL 时时间截断到微秒；表很大时耗时主要在请求日志；
  目标库上的触发器在复制时照常触发（当前只有“`group_id` 为 NULL 时补默认组”一个，复制的行都带有组，不受影响），
  今后如增加会改写数据的触发器，需要在工具中按已知特例处理。

## 后续工作

- SQLite 的 `lower()` / `LIKE` 只折叠 ASCII 大小写；文本排序为字节序。如有需要可注册 Unicode 版本的函数。
