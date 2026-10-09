# ADR-0010：结算、配额与请求日志的持久化（本地 journal）

- 状态：已接受并实施
- 日期：2026-10-09
- 决策人：用户、架构（上线前评估 P0-3）

## 背景

上线前审计（已核实）发现结算链路在数据库故障时会静默丢数据：

- `settle()` 在 `SettleUsage` 失败时只记日志，钱包不扣费、用量计数不增加；
- 套餐额度 `RecordUsage` 同样失败即丢；
- `SweepExpiredReservations` 15 分钟后释放过期预留，被释放的请求从此免费；
- 请求日志写入失败时整批丢弃。

部署约束：**只支持单实例**（ADR-0009 的 SQLite 模式本来如此；PostgreSQL 部署目前也按单实例运维），因此可以使用本地磁盘。

## 可选方案

1. **数据库 outbox 表**：失败的结算写入同一个数据库——数据库不可用时 outbox 也写不进去，解决不了主要场景。
2. **本地追加写 journal + 后台重放**（本 ADR 选择）：不依赖数据库，单实例下简单可靠。
3. 外部消息队列：引入新依赖，与“单镜像、零依赖”的定位冲突。

## 决策

1. **工作项**：一次请求的结算是一个 `gateway.Settlement`（请求 ID、用户、是否结算钱包、实际扣费、用量计数增量、套餐额度记录）；
   请求日志以批为单位。两者都先在进程内退避重试（结算 3 次：0 / 250 ms / 1 s；日志 3 次：0 / 0.5 s / 2 s；journal 已有积压时只试 1 次），
   仍失败才写入 journal，并计入 `omnigate_settlement_failures_total{kind}`。已完成的步骤（例如钱包已结算、只差套餐额度）不会再写入 journal。
2. **journal 文件**：`$OMNIGATE_DATA_DIR/settlement-journal.jsonl`（`production` 默认 `/data`，即镜像的卷；`development` 默认 `./.data`；
   目录 0700、文件 0600，不存在时创建）。每行一个 JSON：条目 `{"v":1,"seq":N,"kind":…,"at":…,"data":…}` 或确认 `{"ack":N}`；每次追加都 fsync。
   全部确认后截断为空文件；确认行占多数时原子重写（临时文件 + rename + 目录 fsync）。
3. **重放**：启动时立即重放，之后由后台 worker 每 5 秒检查一次（失败时退避到最长 1 分钟），按 `seq` 顺序逐条执行，遇到失败即停止，保持顺序。
   条目执行成功后写确认行。数据库可用（`Ping` 成功）而同一条目连续失败 5 次时，移到 `.dead` 文件并记错误日志，避免一条坏数据阻塞后续条目；
   找不到处理器的条目直接移入 `.dead`。
4. **幂等**（每个步骤按请求 ID 去重，重放多少次都只生效一次——崩溃发生在提交之后、确认之前时会再次重放）：
   - 扣费：账本唯一索引 `ledger_entries_charge_ref_uidx (ref_type, ref_id) WHERE kind = 'charge'`（已有）；重复扣费时用量计数也跳过；
   - 不扣钱包的结算（套餐覆盖、自有 / 共享渠道、未开启强制计费）的用量计数：新增 `settled_requests(request_id PRIMARY KEY, settled_at)`
     （迁移 `00016_settlement_dedupe`，两种数据库），与计数在同一事务内插入，已存在则跳过；保留 60 天，随每日的日志保留任务清理；
   - 套餐额度：`subscription_charges.request_id` 唯一（已有）；
   - 请求日志：主键 `(id, started_at)` 已唯一且包含 PostgreSQL 的分区键，重放（以及进程内第二次起的重试）使用
     `INSERT … ON CONFLICT (id, started_at) DO NOTHING`，无需新的唯一约束。
5. **预留过期**：继续由 sweeper 在 TTL 后释放过期预留（不延长）。扣费不依赖预留——`SettleUsage` 在没有预留时直接按实际金额扣费，
   因此 journal 中的结算在恢复后仍会扣费，请求不会因预留过期而免费（有集成测试覆盖）。
6. **损坏处理**：文件末尾不完整的一行（写入中途崩溃，调用方未收到成功）在启动时忽略、记警告并截掉；其他无法解析的行移到 `.corrupt` 文件并记错误。
   写入失败时把文件截回写入前的长度，保证下一行从干净的位置开始。journal 也写不进去时（磁盘满），把完整的工作项 JSON 写进错误日志，便于人工补录。
7. **可观测性**：`/readyz` 在 journal 有积压时返回 200 与 `"status":"degraded"`（继续服务），并始终带 `journal.pending`；指标
   `omnigate_journal_pending`、`omnigate_journal_replayed_total{kind}`、`omnigate_settlement_failures_total{kind}`、`omnigate_journal_dead_total{kind}`。
   `omnigate_request_log_dropped_total` 只表示内存缓冲区满时的丢弃。
8. **停机顺序**：先等网关在途请求与结算（`gw.Wait`），再停 worker（日志 writer 排空，失败时写 journal），最后在 `App.Stop` 中 fsync 并关闭 journal。
   未重放的条目留在磁盘，下次启动重放。

## 影响与代价

- 单实例限定：两个进程不能共享同一个数据目录；未来多实例需要改为每实例独立目录或改用共享的持久化队列。
- 重放的结算不再发送支出上限提醒（只影响通知，不影响扣费与计数）。
- 数据库故障期间用户余额与用量暂时不更新（可能短暂超额使用），恢复后补扣，余额可能变为负数（与“结算按实际用量”的既有规则一致）。
- 新增一张小表 `settled_requests`，每个不扣钱包但有用量计数的请求写一行。
- 测试：journal 单元测试（追加、重启、部分确认、末行损坏、坏行、死信、压缩、并发）；集成测试（两种数据库）覆盖结算与日志写入失败 → 写入
  journal → `/readyz` degraded → sweeper 释放过期预留 → 恢复后重放只扣一次、只写一次日志、套餐额度只计一次 → 模拟“提交后未确认”再次重放
  仍只生效一次，以及关闭连接池的真实故障 + 新进程在同一数据目录启动时重放。

## 后续工作

- 多实例部署时的方案（见上）。
- 在控制台显示 journal 积压与死信数量。
