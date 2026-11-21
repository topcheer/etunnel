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
| 2026-11-21 17:50 | 36ccc54（构建自 main HEAD a24a750，= 36ccc54 + docs-only #300，二进制等价） | 33ffb63abe7987920857bddfd02f99e98eef1e4a9181df3707276b72ca55139b | 四件全过：is-active / 443:80 监听（pid 3508333）/ 本机 `-k` 200 / 公网 t.etun.uk 200；双项验收：`agentprompt`×3 + `f8f3e7`×5；60s err 观测无新增；`go test ./server/` PASS（8.05s） | 成功，agent 区块 + SEC 三连 #292/#293/#294 一轮全包，备份 `etunneld.bak-pre36ccc54-20261121`（9a71dcd），安全债清偿 |
