# ADR-0008：凭据加密与密钥轮换

- 状态：已接受
- 日期：2026-10-08

## 决策

- 主密钥由环境变量 `OMNIGATE_MASTER_KEY` 提供，格式 `<kid>:<base64(32 字节)>`，可配置多个，**第一个为当前加密密钥**，
  其余只用于解密旧数据。生产环境缺少主密钥时拒绝启动；开发环境会生成临时密钥并告警。
- 子密钥派生：`HKDF-SHA256(master, info="omnigate/<purpose>")`，不同用途（`channel-secret`、`oauth-state`、
  `plugin-storage`、`redeem-code` 等）互不共用密钥材料。
- 加密算法：AES-256-GCM，随机 96 bit nonce；密文格式 `v1:<kid>:<base64url(nonce||ciphertext)>`。
- **关联数据（AAD）绑定位置**：例如 `channel_secret:<channel_id>:<field>`，防止把一行的密文复制到另一行或另一个字段后被成功解密。
- 轮换：把新密钥加到列表最前面并重启 → 新数据用新密钥加密；后台任务（Phase 1 随渠道密钥一起提供）把 `NeedsRotation()` 为真的数据重新加密；
  完成后再移除旧密钥。
- 密钥字段永不通过读取接口返回；日志、审计、错误信息中不得出现明文（Phase 1 增加日志脱敏扫描测试）。
- 兑换码、Gateway Key 这类“只需比对、不需还原”的秘密**不加密存储，只存 SHA-256 摘要**。
  两者都是高熵随机值（Gateway Key 256 bit、兑换码 100 bit），无法通过离线穷举还原，因此不需要 HMAC；
  不用 HMAC 也避免了主密钥轮换导致所有 Key 和兑换码失效。（2026-10-08 修订：原文写的是 HMAC。）

## 影响

- 主密钥丢失 = 所有已加密的上游凭据无法恢复。运维文档要求把主密钥和数据库备份分开保管。
- 未来可以替换为 KMS/Vault 后端（`secretbox` 接口保持不变）。
