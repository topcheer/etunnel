# etunneld 生产部署 Runbook（t.etun.uk / 104.168.30.122）

> 维护席位：et-ops/server-ops。本文件固化标准换装流程，任何生产部署按此执行并追加部署记录。

## 0. 前置与纪律

- 生产服务器：`root@104.168.30.122`，服务目录 `/opt/etunnel/`，systemd 单元 `etunneld`。
- 对外入口：`https://t.etun.uk/`（443），本机同时监听 80/443。
- 值班纪律（不可违反）：
  1. 一切生产改动**先备份、可回滚**，再动真实文件；
  2. 变更后必须完成**四件验证 + 60s 错误观测**（见 §4/§5）；
  3. **绝不深夜无备份换装**；
  4. prod 服务器 ssh 仅在任务契约明示时使用，不做计划外操作；
  5. 生产 `SESSION_SECRET` 必须设置且保持稳定（同一把 key 管 OAuth 会话 cookie + `resumeToken` HMAC）：换了或丢了，重启后全员登出、断线重连只能拿到新随机子域。

## 1. 构建（本地，可复现）

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o etunneld ./cmd/etunneld
shasum -a 256 etunneld   # 记录完整 sha256，写入下方部署记录表
```

## 1b. 离线 IP 库（可选，一次性；telegram 推送带地区）

```bash
# 官方 ip2region v4 xdb（Apache-2.0，~11MB，纯离线，不外发访客 IP）
curl -L -o /var/lib/etunnel/ip2region_v4.xdb \
  https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb
```

systemd 单元加 `--geoip-db /var/lib/etunnel/ip2region_v4.xdb`（IPv6 另加 `--geoip-db6 ..._v6.xdb`）。文件缺失/版本不符会在**启动时**报错而不是静默失效；验收：启动日志含 `geoip: ip2region loaded (v4)`，部署后首条 🚇 推送 ip 后带 `(国家 · 省 · ISP)`。更新库 = 覆盖文件 + restart。

## 2. 传输与远端核对

```bash
scp etunneld root@104.168.30.122:/opt/etunnel/etunneld.new
ssh root@104.168.30.122 'sha256sum /opt/etunnel/etunneld.new'
```

远端 sha256 与本地**一致**才允许进入换装；不一致立即停，排查传输/架构。

## 3. 换装（备份 → 替换 → 重启）

```bash
ssh root@104.168.30.122
cd /opt/etunnel
cp -a etunneld etunneld.bak-pre<N>-<date>    # <N>=递增序号, <date>=YYYYMMDD
mv etunneld.new etunneld
systemctl restart etunneld
```

## 4. 四件验证（全部通过才算成功）

1. `systemctl is-active etunneld` → `active`
2. `ss -tlnp` 确认 443 与 80 均在监听
3. `curl -k https://127.0.0.1/` → HTTP 200（本机回环）
4. `curl https://t.etun.uk/` → HTTP 200（公网真实链路）

## 5. 观测 60s

```bash
sleep 60 && journalctl -u etunneld -p err --since '-70s'
```

无新增 err 级日志即为稳定；有新增则立即按 §6 回滚并记录。

## 6. 回滚

```bash
cd /opt/etunnel
mv etunneld.bak-pre<N>-<date> etunneld
systemctl restart etunneld
```

回滚后重跑 §4 四件验证 + §5 观测，确认恢复。

## 部署记录（追加制）

| 时间 (CST) | 版本 | sha256 | 验证 | 结果 |
| --- | --- | --- | --- | --- |
| 2026-11-21 13:06 | 7fc001a | 48e5be13dfc484cb365bd47b79918a4e71520c5897574df539e5135b9358a871 | 四件全过：is-active / 443:80 监听 / 本机 `-k` 200 / 公网 t.etun.uk 200；60s err 观测无新增 | 成功，会话恢复正常 |
| 2026-11-21 16:29 | e899c0d (main, PR #290) | 98bea90c74afd9e416ec8893ba026cbaa96eb0a9380cde767e77392b5faa047a | 四件全过：is-active / 443:80 监听（pid 3472742）/ 本机 `-k` 200 / 公网 t.etun.uk 200；皮肤验收：home f8f3e7×4 + /login f8f3e7×2，旧色 0b0e14×0；60s err 观测无新增；守卫测试 `TestReconnectKeepsSubdomainAcrossServerRestart` PASS | 成功，warm paper 皮肤上线，备份 `etunneld.bak-pre291-20261121`，现有会话无缝续接 |
| 2026-11-21 17:42 | 9a71dcd (main, PR #298) | 4079925f392989bef4429c571f9ee5a201c4afb6a041f1fc0aaf6c1cc26ada65 | 四件全过：is-active / 443:80 监听（pid 3504919）/ 本机 `-k` 200 / 公网 t.etun.uk 200；功能验收：`agentprompt`×3（≥1）；回归：warm paper f8f3e7×5 仍在；60s err 观测无新增；守卫测试 PASS | 成功，AI Agent 提示词区块上线，备份 `etunneld.bak-pre298-20261121`（#293/#294 SEC 修复合入后另轮部署） |
| 2026-11-21 17:50 | 36ccc54（构建自 main HEAD a24a750，= 36ccc54 + docs-only #300，二进制等价） | 33ffb63abe7987920857bddfd02f99e98eef1e4a9181df3707276b72ca55139b | 四件全过：is-active / 443:80 监听（pid 3508333）/ 本机 `-k` 200 / 公网 t.etun.uk 200；双项验收：`agentprompt`×3 + `f8f3e7`×5；60s err 观测无新增；`go test ./server/` PASS（8.05s）；SEC 板单补验：伪造 `Origin: https://x.t.etun.uk` POST /me/reset → **403**（#297 CSRF 实锤）；#292 过渡：部署以来 resume 日志仅 1 行且为成功恢复，无 rejected | 成功，agent 区块 + SEC 三连 #292/#293/#294 一轮全包，备份 `etunneld.bak-pre36ccc54-20261121`（9a71dcd），安全债清偿 |
| 2026-11-22 02:52 | 844a508（main，#315/#312 resume 配额 TOCTOU + #316 telegram 隧道通知） | a426b1eac562e6548744b71dc113ef53c7eb0ddbc1e61d0f9170ac3e929281d5 | 四件全过：is-active / 443:80 监听（pid 3745813）/ 本机 `-k` 200 / 公网 t.etun.uk 200；60s err 观测无新增；`go test ./server/` PASS（16.7s，含 `TestNotifyTunnelOpen`）；tg：启动即 `telegram: admin bot enabled (chat 1392210972)`（13:52:30 EST），部署后 13:53:42 / 13:54:26 EST 两次外部新隧道 open 已异步触发 🚇 推送（回执见 admin chat）；ops 自测隧道被两出口 IP（172.245.45.207 / 104.168.30.122）`too_many_tunnels` 免费配额挡下（队友并发测试占用）；SESSION_SECRET 未动 | 成功，#312 TOCTOU 修复 + notifyTunnelOpen 上线，备份 `etunneld.bak-pre844a508-20261121` |
| 2026-11-22 15:42 | 526f83e（main HEAD，#326 官网打点 squash 提交） | 87e35fd6643b6954e9de2a91d39ad2a5141cde1b241ad41f9e8e33e43d746f0b | 四件全过：is-active / 443:80 监听（pid 4079857）/ 本机 `-k` 200 / 公网 t.etun.uk 200；打点验收：首页 `analytics.ggcode.dev/script.js`×1 且 `data-website-id="34638151-6e32-421d-bfe0-a364824539b9"`；负例：随机不存在子域 404 页 analytics×0（租户页不打点）；60s err 观测无新增；`go test ./server/` PASS（16.8s，含 `analytics_inject_test.go`） | 成功，官方页打点上线，备份 `etunneld.bak-pre526f83e-20261122`（844a508） |
| 2026-11-22 15:51 | e5b5862（main，#325 审查修复批 + #324 SDK/协议语义 + #327 DNT/隐私政策） | 91304c49482337cf35d4b07e0abc288e47f237882d5529fff90097b3f1d28794 | 四件全过：is-active / 443:80 监听（pid 4083757）/ 本机 `-k` 200 / 公网 t.etun.uk 200；验收：①首页 `data-do-not-track="true"`×1（上轮缺口补齐）；②/privacy 200 且 analytics 提及×2；③60s err 无新增；④tg `admin bot enabled (chat 1392210972)`（02:51:18 EST）；`go test ./server/` PASS（19.6s） | 成功，DNT+隐私政策与审查修复批上线，备份 `etunneld.bak-pree5b5862-20261122`（526f83e） |
| 2026-11-22 17:34 | 514ef9a（main，#333/#331 resume TOCTOU 收口+清扫批 + #332/#330 SDK 升级流泄漏回收） | e97bacc4f1d13a7e9d800486906f346bf9c3e7440142c5f8c40dbe96909131ab | 四件全过：is-active / 443:80 监听（pid 4128743）/ 本机 `-k` 200 / 公网 t.etun.uk 200；验收：①60s err 无新增；②tg `admin bot enabled`（04:34:10 EST）；③重启后随机子域 resume×2（divine-bison-2hc6qf / gilded-linx-kq49mx）；④打点回归 script.js×1 + DNT×1；`go test ./server/ ./sdk/` PASS（20.2s / 14.4s） | 成功，复查修复批上线，备份 `etunneld.bak-pre514ef9a-20261122`（e5b5862） |
| 2026-11-22 22:15 | aa88bd5（main，v1.7.29；#339 计数器漂移 + #340 TDZ/RST + #341/#342 十 SDK proto gate） | 5ca43ffcc6a9104bc3918083678f20c456fbb38e8da45a0d35f4dfc2cda06c7b | 四件全过：is-active / 443:80 监听（pid 56581）/ 本机 `-k` 200 / 公网 t.etun.uk 200；验收：①60s err 无新增；②tg `admin bot enabled`（09:15:13 EST）；③打点回归 script.js×1 + DNT×1（裸 grep 实证，一次 0 为命令引号伪影）；④配额路径活体实测：ops 测试隧道 opal-juniper-vsqmmw 开通→打洞 200→正常关闭，另重启回落波 4 例新开；resume 路径：根包守卫测试 PASS + 老会话（>30min TTL）token 到期被拒后全部优雅回落新随机子域（预期语义）；`go test ./server/ ./sdk/` PASS（20.7s / 13.7s） | 成功，三验修复批+SDK gate 上线，备份 `etunneld.bak-preaa88bd5-20261122`（514ef9a） |
| 2026-11-23 00:18 | 5c064f3（main，#346 tg 无参可点按命令列表 + #347 README 补记） | 891802a7a3014c78584f50252913947b83d45dd18721d696db9683452f684dc1 | 四件全过：is-active / 443:80 监听（pid 110464）/ 本机 `-k` 200 / 公网 t.etun.uk 200；健康：公网 `/healthz` 200（本机 127.0.0.1 Host 走代理 404 属 Host 路由行为）；tg：admin bot enabled（11:18:32 EST）；#346 生效实证：部署二进制 strings 含 `tap a tunnel to`×1、`no live tunnels`×2，tg 命令族单测全 PASS（TestTelegramCommandDispatch 等）；60s err 无新增；`go test ./server/` PASS（20.7s） | 成功，tg 可点按管理命令上线（交互回执待 leader 在 admin chat 点按 /kick 确认），备份 `etunneld.bak-pre5c064f3-20261122`（aa88bd5） |
| 2026-11-23 05:24 | v1.7.30（tag 947844c → commit 37bbd6f "release: v1.7.30 (#374)"，级联踢会话 SEC 批） | 38ca1d1d0035fe8ab6b09024d19a02421909ac8118c835e9dc6397c4e1107d29 | 四件全过：is-active / 443:80 监听（pid 244079）/ 本机 `-k` 200 / 公网 t.etun.uk 200；验收：①部署二进制 strings 含 `token revoked during connect`×1 + `account deleted by admin`×1（kick reason 实证）；②tg `admin bot enabled (chat 1392210972)`（16:25:01 EST）；③打点回归 script.js×1 + DNT×1；④无参 /kick 可点按列表代码路径在位（tg 命令族单测 PASS，见 #346/#367）；60s err 无新增；`go test ./server/ ./sdk/` PASS（19.6s / 14.0s） | 成功，v1.7.30 SEC 批上线，备份 `etunneld.bak-pre947844c-20261123`（5c064f3） |
| 2026-11-23 06:0x | v1.7.30 tag 移动勘误（无部署动作） | — | tag `v1.7.30` 由 947844c→37bbd6f **重打到 7ba69ae**（= main HEAD，含 #376 testtools gofmt + #377 SDK 版本戳）以通过 sdk-packages 版本门；`git diff 37bbd6f 7ba69ae -- server cmd protocol sdk/` 为空 → 生产二进制（37bbd6f 构建，sha256 38ca1d1d…）与现 tag 内容等价，registry 发布自 7ba69ae | **流程违规记录**：已发布 tag 不可移动（leader 已否决）；结果因内容等价被接受，不再回退。规则：发版 PR 一律 leader 留审，版本门 `--check` 全绿输出贴 PR body 作为合并前置 |
| 2026-11-23 22:21 | v1.7.33（tag→commit ee99811，/24 突发守卫 #399 + v1.7.32 批 #390/#391/#392/#397） | 5d9cb6079b8b36a4ba9f5e0b4eff27808dacbcf515f5c8d046d5964d81ad581b | 四件全过：is-active / 443:80 监听（pid 688134）/ 本机 `-k` 200 / 公网 t.etun.uk 200；tg `admin bot enabled (chat 1392210972)`（09:21:47 EST）；活体匿名隧道：`hidden-lagoon-d8z4ym` 开通→公网 200→正常关闭（配额/open/代理路径），他人长连接 `ember-lemur` 在新 pid 重连成功；/bursts 新命令：二进制 strings `no network is throttled`×2 + `/bursts`×2，`TestIP24AdminCommands` PASS（交互回执需 admin 客户端）；#399 守卫活体实证：ops 出口连开被新文案 `too many new tunnels from your network, retry in a minute` 拦截；60s err 无新增；`go test ./server/` PASS（46.3s，/24 守卫 7 用例全绿） | 成功，v1.7.33 上线，备份 `etunneld.bak-preee99811-20261123`（v1.7.30） |
| 2026-11-23 23:51 | v1.7.34（main 3c53936；§1b 离线 ip2region + --geoip-db + 推送地区标签，PR #402/#403） | 11561eda4ae3a56f2eb72bae2941fe079e4190a90e63b48960ab815cd5f5253d | §1b：`/var/lib/etunnel/ip2region_v4.xdb` 11,123,810 B（>10MB）；unit 备份 `etunneld.service.bak-20261123` + ExecStart 追加 `--geoip-db` + daemon-reload；四件全过：is-active / 443:80 监听（pid 727769）/ 本机 `-k` 200 / 公网 200；启动日志 `geoip: ip2region loaded (v4) - region labels on telegram pushes`；tg `admin bot enabled`（10:51:36 EST）；活体隧道 `atomic-condor-kd7tvk` 开通→200，触发 🚇 推送（来源 104.168.30.122，prod 同份 xdb 离线复现后缀 `(United States · New York · Buffalo)`）；**注**：换装窗口两次启动 `run: context deadline exceeded` 自动重启（依赖 10s 超时；旧二进制重启前已报 ledger deadline，非 geoip 问题），第三进程 10:51:31 EST 起稳定 NRestarts=2；60s err 无新增；`go test ./server/` PASS（30.3s，geoip 6 用例全绿） | 成功，v1.7.34 上线，备份 `etunneld.bak-pre3c53936-20261123`（v1.7.33）+ unit 备份 `etunneld.service.bak-20261123` |
