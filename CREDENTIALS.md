# etunnel 运营配置清单（你需要提供的凭据）

所有功能均为 **env 驱动、留白即关闭**——不需要的功能不用提供任何东西。

## 1. 支付（管理站/用户站订阅）

| 变量 | 哪里拿 | 不填的后果 |
|---|---|---|
| `STRIPE_SECRET_KEY` | dashboard.stripe.com → Developers → API keys（`sk_live_`/`sk_test_`） | Card 支付按钮 503 |
| `STRIPE_WEBHOOK_SECRET` | 同上 → Webhooks → 添加端点 `https://<域名>/webhooks/stripe`（事件：checkout.session.completed）→ 得 `whsec_` | 支付后激活只靠回跳页（建议配） |
| `PAYPAL_CLIENT_ID` | developer.paypal.com → Applications → Create App（Live） | PayPal 按钮 503 |
| `PAYPAL_SECRET` | 同上 App 详情 | 同上 |
| `PAYPAL_ENV` | 生产留空（=live），测试填 `sandbox` | — |
| `PAYPAL_WEBHOOK_ID` | 同 App → Webhooks → 添加 `https://<域名>/webhooks/paypal`（事件：Payment capture completed） | 同 Stripe（兜底） |
| `TIER_STARTER_PRICE_CENTS` / `TIER_PRO_PRICE_CENTS` | 自定（默认 300 / 900） | 用默认价 |

## 2. 用户登录（订阅前置）

### Google
console.cloud.google.com → APIs & Services → Credentials → OAuth client ID（Web application）
- Authorized redirect URI：`https://<域名>/auth/google/callback`
- 提供：`GOOGLE_CLIENT_ID`、`GOOGLE_CLIENT_SECRET`

### GitLab（自建或 gitlab.com）
gitlab.com → User Settings → Applications（或自建实例同路径）
- Redirect URI：`https://<域名>/auth/gitlab/callback`，勾 `read_user`
- 提供：`GITLAB_CLIENT_ID`、`GITLAB_CLIENT_SECRET`（自建另加 `GITLAB_URL`）

### Apple
developer.apple.com → Certificates, IDs & Profiles → Identifiers → 注册 App ID（勾 Sign in with Apple）→ 创建 Services ID + 关联 Key
- Return URL：`https://<域名>/auth/apple/callback`
- 提供：`APPLE_CLIENT_ID`（Services ID）、`APPLE_TEAM_ID`、`APPLE_KEY_ID`、`APPLE_PRIVATE_KEY`（.p8 文件全文，含 BEGIN/END 行，换行用 `\n`）

### 会话
- `SESSION_SECRET`：任意 ≥32 字节随机串（不填则每次重启所有人重新登录）

## 3. Telegram 管理机器人

| 变量 | 哪里拿 |
|---|---|
| `TELEGRAM_BOT_TOKEN` | Telegram 里找 @BotFather → `/newbot` |
| `TELEGRAM_ADMIN_CHAT_ID` | 找 @userinfobot 发条消息，回你的数字 id |
| `TELEGRAM_ALERT_TUNNELS`（可选） | 隧道数告警阈值（如 `50`；不填=关） |

菜单点按即用：📊状态 / 🚇隧道 / 👥账户 / 🔍归属 / ⬆️改档 / ⛔踢线 / 🔑key / 🗑删号；支付成功与失败、阈值越线自动推送。

## 4. 部署

凭据写入服务器（示例）：`/etc/etunnel/billing.env`，systemd 单元加：

```
[Service]
EnvironmentFile=/etc/etunnel/billing.env
```

然后 `systemctl restart etunneld`。验证：`/stats` 里 `billing` 段变 true；`/pricing` 能拉起收银台；Telegram 发 `/status` 有回。

## 5. 尚欠的 registry 侧凭据（发布线，非运营）

| 项 | 动作 |
|---|---|
| hex.pm Workload Identity | hex.pm/packages/etunnel → Workload identities → Add：owner `topcheer` / repo `etunnel` / workflow `sdk-packages.yml` / env `hex`（需 2FA） |
| rubygems CI 化 | 等 rubygems.org 对 OIDC 存量 gem 403 的澄清；期间发版手动 `gem push`（你在场过 WebAuthn） |
| CPAN | PAUSE 审批通过后把用户名密码给我接线 |
