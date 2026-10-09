# ADR-0004：租户层级——三级作用域 + 预留 workspace

- 状态：已接受（2026-10-08 用户确认）
- 日期：2026-10-08

## 决策

1. 首期只做 **系统 / 管理员 / 用户** 三级。资源作用域只有 `private`（所有者）、`shared`（指定用户或角色）、
   `global`（管理员发布给所有符合条件的用户）。
2. 核心表（`users`，以及后续的 `channels`、`gateway_keys`、`plans`、`wallets` 等）都带一个可为空的 `workspace_id uuid`，
   当前恒为 NULL。将来引入工作区/项目时，现有数据归入每个用户的“个人工作区”，**不需要破坏性迁移**。
3. 角色：`system_admin`、`channel_admin`、`user`、`auditor`，作为 `users.role` 列保存；
   角色与权限的映射**首期在代码中定义**（`server/internal/authz`），不建 `roles`/`permissions` 表。
   如果以后要支持自定义角色，再迁移到表驱动（届时新增 ADR）。
4. 授权判断 = `role + action + resource + scope`（`authz.Principal.CanOnResource`）：
   - 所有者对自己的资源拥有全部操作权。
   - 持有 `*.manage` 权限的管理员可以管理他人的资源。
   - `shared`/`global` **只授予“使用（通过网关调用）”**，永远不授予“查看配置”或“读取密钥”。
5. 所有判断都在服务端完成；`/api/me` 返回的权限列表只用于前端显示/隐藏入口。
6. 保护措施：不能降级或停用最后一位系统管理员；不能停用自己；用户被停用后，其会话在下一次请求即失效。

## 影响

- 团队共享首期通过 `shared` 作用域实现（指定用户/角色），足以覆盖个人与小团队。
