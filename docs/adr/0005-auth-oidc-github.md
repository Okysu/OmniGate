# ADR-0005：身份认证——仅外部身份（通用 OIDC + GitHub 适配器）

- 状态：已接受（2026-10-08 用户确认）
- 日期：2026-10-08
- 取代：原需求中的“邮箱/用户名 + 密码注册登录、邮箱验证、忘记密码”

## 背景

用户要求不自行实现账号密码注册登录，只使用 OIDC，并以 GitHub 作为首个登录方式。
**事实澄清**：GitHub 面向用户登录只提供 OAuth2（OAuth App / GitHub App），不签发 ID Token，也没有
`/.well-known/openid-configuration`。GitHub 的 OIDC 只用于 Actions 工作负载身份，不能用于用户登录。

## 决策

1. 登录层按**通用 OIDC** 设计（`coreos/go-oidc`：discovery、ID Token 签名校验、nonce、PKCE），
   任何标准 IdP（Keycloak、Authentik、Zitadel、Google、Azure AD 等）只需配置即可接入，支持多个。
2. 另外内置 **GitHub OAuth2 适配器**：授权码 + PKCE（S256），用访问令牌调用 `GET /user` 与 `GET /user/emails`；
   身份主键使用 GitHub **不可变数字 ID**（`subject`），`login` 仅用于展示和匹配规则。只信任“已验证的主邮箱”。
3. **不存储任何密码**；没有本地注册、邮箱验证、忘记密码流程（由 IdP 负责）。
4. 账号开通（admission）：
   - `OMNIGATE_BOOTSTRAP_ADMINS` 中的身份首次登录即成为系统管理员（推荐写 `github-id:<数字ID>`）。
   - 注册模式：`open`（任何通过 IdP 认证的身份）/ `restricted`（默认；仅允许名单与邮箱域名）/ `closed`（仅已有账号）。
   - 匹配语法：`github:<login>`、`github-id:<id>`、`<oidc-id>:<sub>`、`email:<已验证邮箱>`。
   - 开发模式下若未配置引导管理员，**第一个账号**成为系统管理员；生产模式未配置引导管理员时拒绝启动。
   - 邀请码：后续可复用兑换码体系（ADR-0006）中的 `invite` 类型，首期不实现。
5. 会话：
   - 登录成功后签发 256 bit 随机令牌，放在 `og_session` Cookie 中（`HttpOnly`，生产环境强制 `Secure`，`SameSite=Lax`）；
     数据库只保存 SHA-256 哈希。
   - 绝对有效期默认 30 天，空闲超时默认 7 天；支持查看会话、撤销单个会话、退出其他设备。
   - OAuth `state`/`nonce`/PKCE verifier 放在加密 Cookie 中（AES-GCM，10 分钟，单次使用），服务端无状态。
6. CSRF：Cookie 认证的非 GET `/api/*` 请求必须带 `X-Requested-With: XMLHttpRequest`，并且如果带 `Origin`，
   必须是允许的来源。配合 `SameSite=Lax` 形成纵深防御。CORS 默认关闭。
7. 登录、回调接口按 IP 前缀限流；登录成功、拒绝、退出、撤销会话都写审计日志。
8. 关联多个身份（同一用户绑定 GitHub + 公司 SSO）：数据模型已支持（`user_identities`），
   “已登录用户绑定新身份”的流程放到 Phase 3；**不做按邮箱自动合并账号**（防止账号接管）。

## 影响

- 网关 API Key（`/v1/*` 调用）与浏览器会话完全独立，SDK 调用不受 OIDC 影响。
- 本地开发使用 `mock-oauth2-server`（标准 OIDC）验证通用路径；GitHub 路径在测试里用伪造的 GitHub 服务覆盖。
