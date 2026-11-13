# Feature Gap Analysis — 极简集成 tunnel 平台还缺什么

日期：2026-11-13 ｜ 方法：六家竞品 web 调研矩阵 × 本仓协议/入口/CLI 实证 × 极简定位过滤
仓库实证已于 v1.6.12 发版后复核（2026-11-13）确认仍成立。
（竞品：ngrok、Cloudflare Tunnel、localhost.run、localtunnel、boringproxy、zrok）

## 现状盘点（已有的，比多数竞品强）

- 20 门语言 SDK 一行集成（全场最宽）
- 零注册即用；CLI login（RFC 8252 loopback）+ passkey
- 付费固定子域体系（含 1-3 字符短域）+ resume token 断线保名
- 多支付分账（Stripe/PayPal/NOWPayments）、mesh 集群、带宽/TTL/流控分层
- TLS 三模式（ondemand/dns/static）+ ACME 自动证书

## 实证确认的真缺口（仓库逐项查证，非猜测）

| # | 缺口 | 竞品覆盖（修正口径） | 本仓实证 |
|---|---|---|---|
| ① | 隧道层 basic auth / IP allowlist | ngrok、zrok、Cloudflare Access（boringproxy 无内建证据，不计入） | grep 全 server/ 无隧道层 auth（仅支付 provider 出站用） |
| ② | raw TCP 隧道（SSH/DB） | **公开可用口径 ≈ 3/6**：ngrok、zrok（配对式）、localhost.run（付费 Pro）；Cloudflare 公网 TCP 走 Spectrum 企业档，boringproxy 仅 HTTPS 端口上 SNI 分流的 TLS 流（issue #36 时代任意端口 TCP 长期缺），localtunnel 无 | 协议帧已是纯字节流复用（DATA/FIN/RST），缺的只是入口 TCP 监听层——底座现成 |
| ③ | config 文件 / 多隧道声明 | ngrok.yml、cloudflared config.yml 标配 | CLI 仅 flags+env，一进程一隧道 |
| ④ | 自有域名绑定（CNAME+代管证书） | ngrok/zrok/localhost.run/boringproxy（4/6，多在付费档） | 仅子域+短域 |
| ⑤ | 请求检查/回放 | 仅 ngrok（Inspector，护城河级） | 无 |

### 口径脚注（vs 初版矩阵的修正）

- "raw TCP 5/6 家有"系初版矩阵宽口径；按**公开可用的 raw TCP 端点**重数为 ≈3/6（见上表）。不影响"值得做"的结论。
- "隧道层 auth 三家均有"中 boringproxy 一项未找到内建 basic auth 的官方证据，已移出该行。

## 刻意不做也不丢分（极简定位）

- 多区域 edge（单区 + CDN 前置可替代）
- 团队 RBAC/子 key 权限（个人开发者定位）
- ngrok 级完整 Inspector（投入产出不成比）

## 排期建议（按 集成价值/实现成本 排序）

1. **隧道层 auth**（`{auth:"user:pass"}` + IP allowlist）——边缘 401 中间件，成本最低；覆盖"demo 不想裸奔"最高频诉求；20 SDK 各加一个 option
2. **免费 1 个静态子域**——纯产品策略改动（子域体系现成）；ngrok 2023 免费送静态域是其获客转折点，URL 可复用直接拉留存
3. **webhook 验签助手**（Stripe/GitHub 验签+头部重写）——全场仅 ngrok 有；对"tunnel 即集成工具"叙事是高价值差异化，标准库工作量
4. **raw TCP 模式**——协议底座已备（字节流复用），加端口监听+按端口路由；TAM 从 HTTP demo 扩到 SSH/DB/游戏服
5. **减配版请求回放**（最近 N 条元信息+body 样本+生成 curl）——价值最高成本也最高，"daily devtool"的钥匙

建议 1+2 先行（一周内可全量交付含 20 SDK），3/4 次之。
