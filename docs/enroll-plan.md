# 方案 A 实现方案：一条指令批量部署自注册探针

> 目标：**同一条指令**在任意多台 Linux 子机上执行，每台自动注册为**独立节点**、自动完成命名，
> 无需在面板逐个「添加节点」。

---

## 0. 实测事实（决定方案形态）

这些不是推断，是在真实机器上探测得到的：

| 事实 | 证据 |
| --- | --- |
| Lite-agent **2.3.3.5 已移除注册能力** | 官方 release 二进制中 `--auto-discovery` 参数不存在；`/api/clients/register`、`registerWithAutoDiscovery`、`Auto-discovery config saved` 等符号与字符串**全部为空** |
| 旧版 komari-agent **1.5.11 仍有注册能力**（可作参照） | 实测它的注册请求与身份落盘行为 |
| 旧版注册的**线上协议**（已实测） | `POST /api/clients/register?name=<hostname>`，头 `Authorization: Bearer <key>`，体 `{"key":"<key>"}`；期望响应 `{"status":"success","data":{"uuid":"...","token":"..."}}` |
| 旧版身份文件 | agent 工作目录下 `auto-discovery.json`，内容 `{"uuid":"...","token":"..."}`（已实测生成） |
| Lite 服务端注册端点**已被移除且有守卫测试** | `web/router/router_test.go:119 TestRegisterDoesNotExposeAutoDiscoveryRegister`；`AutoDiscoveryKey` 被标注 "unused" 并在 `admin.misc.go:130` 剥离 |
| **agent 支持 JSON 配置文件** | `--config` 参数存在；配置项含 `endpoint`/`token`/`interval`/`remote_control_enabled` 等（Lite-agent README 明确） |
| agent 安装脚本会把非 `--install*` 参数**透传给 agent 并写进服务单元** | `install.sh` 的 `agent_args` 收集逻辑 + `--config` 被 `preserve_existing_config_arg` 识别 |
| agent 启动时日志确认会用配置 | 实测：`Using existing auto-discovery token for UUID: ...`、`Monitoring Interfaces: ...` |

**结论**：不恢复被删除的注册端点，也不需要改动 agent 二进制。
改为**"服务端签发 + 脚本写入 agent 配置文件"**：新增一个 enroll HTTP 端点 + 一个由 Lite 自己托管的安装脚本。

优点：不动 agent、不复活旧协议、不与上游守卫测试冲突、改动集中在 Lite 仓库。

---

## 1. 目标行为

子机上执行（同一条，N 台通用）：

```bash
curl -fsSL https://<面板域名>/install/agent.sh | sudo bash -s -- \
  -e https://<面板域名> \
  -k <ENROLL_KEY>
```

脚本完成：

1. 生成机器指纹（`/etc/machine-id`，回退 `hostname`）
2. 调 `POST /api/clients/enroll`（带 enroll key + 指纹）→ 拿到该机专属 `uuid` + `token`
3. 写 `/opt/lite-agent/config.json`：
   ```json
   { "endpoint": "https://<面板域名>", "token": "<专属token>", "interval": 600,
     "remote_control_enabled": true, "disable_auto_update": false }
   ```
4. 调官方 `install.sh --config /opt/lite-agent/config.json --enable-remote-control`
5. agent 启动 → 正常上报 → 触发已有的自动命名 → 名称变为 `国家代码-IP-ASN-ISP`

**幂等**：同一台机器重复执行 → enroll 端点按指纹返回**同一个节点与 token**，不产生重复节点。

---

## 2. 服务端改动（Lite 仓库）

### 2.1 新增数据模型 `database/models/enrollment.go`

```go
type EnrollmentKey struct {
    ID           string     // uuid，主键
    Name         string     // 备注，例如 "机房A-2026Q1"
    KeyHash      string     // sha256(明文 key)，不存明文
    Prefix       string     // 明文前 6 位，仅用于后台列表辨识
    MaxUses      int        // 0 = 不限
    UsedCount    int
    ExpiresAt    time.Time  // 过期时间
    AllowedCIDRs string     // 逗号分隔允许来源网段，空 = 不限
    RevokedAt    *time.Time
    CreatedBy    string
    CreatedAt    time.Time
}

type EnrolledNode struct {   // 指纹 → 节点 的映射，用于幂等
    Fingerprint string     // sha256(machine-id + keyID)
    ClientUUID  string
    KeyID       string
    LastSeenAt  time.Time
    ReportedAt  *time.Time // 被注册节点是否已首次上报（用于清理孤儿）
    CreatedAt   time.Time
}
```

两处登记（否则表不会建）：`database/dbcore/dbcore.go` 的 `instance.AutoMigrate(...)` 列表。

### 2.2 新增端点 `web/api/public/enroll.go`

```
POST /api/clients/enroll
Header: Authorization: Bearer <ENROLL_KEY>
Body:   {"fingerprint":"<sha256>","hostname":"<host>"}
200:    {"status":"success","data":{"uuid":"...","token":"...","name":"..."}}
```

处理顺序（任一失败即拒绝，**都不创建节点**）：

1. 限流（复用 `loginLimitByPair` 同款内存限流：按 IP 计数）
2. 解析 key → 查 `EnrollmentKey`：存在、未撤销、未过期
3. 来源 IP 若在 `AllowedCIDRs` 内有配置，必须命中
4. `MaxUses` 未超（超限返回 403 并审计）
5. 查 `EnrolledNode`：
   - 命中 → 直接返回该节点的 `uuid`/现有 `token`（**不新建**）
   - 未命中 → 在**单个事务**内创建 `clients` 行（占位名 `client_<uuid8>`、`token` 用 `utils.GenerateToken()`）+ 写 `EnrolledNode` + `UsedCount++`
6. 审计日志：`enroll key <prefix> -> node <uuid> from <ip>`

注册路由（`web/router/router.go` 公开路由区）：

```go
r.POST("/api/clients/enroll", public_api.Enroll)
```

> **不要**复用 `/api/clients/register` 这个名字——`router_test.go:119` 有守卫测试断言该路径不得存在，
> 换个路径可以避免与上游测试冲突。

### 2.3 托管安装脚本 `web/install/agentscript.go`

- 用 `go:embed` 嵌入 `web/install/agent.sh`
- 新增路由 `GET /install/agent.sh`，`Content-Type: text/x-shellscript`，`Cache-Control: no-store`
- 脚本内容（要点）：
  ```sh
  #!/bin/sh
  set -eu
  # 解析 -e/--endpoint、-k/--enroll-key
  FP=$(sha256sum /etc/machine-id 2>/dev/null | cut -d' ' -f1 || hostname | sha256sum | cut -d' ' -f1)
  RESP=$(curl -fsS -X POST "$EP/api/clients/enroll" \
           -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
           -d "{\"fingerprint\":\"$FP\",\"hostname\":\"$(hostname)\"}")
  # 用 python3/grep 提取 token（不依赖 jq）
  mkdir -p /opt/lite-agent && umask 077
  printf '%s' "$CFG" > /opt/lite-agent/config.json
  # 再拉官方 install.sh 并带 --config 执行
  ```
- **脚本里不硬编码任何密钥**：key 由命令行传入，token 现场领取

### 2.4 后台管理 RPC `web/rpc/jsonrpc/admin.enroll.go`

| 方法 | 作用 |
| --- | --- |
| `admin:createEnrollmentKey` | 参数 `{name, max_uses, expires_in_hours, allowed_cidrs}`；**仅此一次返回明文 key** |
| `admin:listEnrollmentKeys` | 列出（只给 `prefix`、用量、到期、状态） |
| `admin:revokeEnrollmentKey` | 撤销 |
| `admin:listEnrolledNodes` | 指纹↔节点 映射，便于排障 |

全部 `rpc.RoleAdmin`，并对 `createEnrollmentKey` 调 `rpc.MarkSensitive(...)`（与 `admin:getClientToken` 同级）。
路由挂在 `/api/admin/enroll/*`。

### 2.5 清理与回收（`cmd/app.go` 的 `cleanupScheduledData`）

- 已注册但**从未上报**（`ReportedAt IS NULL`）且超过 60 分钟的节点：删除节点 + 映射，回滚 `UsedCount` → 防止 key 泄露后被灌垃圾节点
- 已上报节点的 `ReportedAt` 在 `ingestBasicInfo` 路径中回填

---

## 3. 安全模型（这是本方案的核心权衡）

| 风险 | 缓解 |
| --- | --- |
| Enroll key 泄露 = 任何人都能注册节点 | `MaxUses` 上限 + `ExpiresAt` 强制过期 + `AllowedCIDRs` 来源网段白名单 + 随时撤销 |
| 成为 DoS/资源耗尽入口 | 端点限流；`MaxUses` 硬上限；60 分钟孤儿回收 |
| key 被日志/历史记录泄露 | 只存 `sha256`；后台仅显示 `prefix`；审计只记 prefix 与来源 IP |
| 明文 token 在响应中被中间人窃取 | 强依赖 HTTPS；明文 key 仅在创建时展示一次 |
| 与现有鉴权体系混用造成越权 | 该端点**只**返回"新建节点的 token"，不接受任何节点 UUID 参数，无法读取/影响已有节点 |

**默认关闭**：新增配置键 `enroll_enabled`（默认 `false`）。未显式开启时端点直接 404，与当前行为一致。

---

## 4. 需要改动的文件清单

| 文件 | 改动 |
| --- | --- |
| `database/models/enrollment.go` | 新增两个模型 |
| `database/dbcore/dbcore.go` | AutoMigrate 列表加入两个模型 |
| `web/api/public/enroll.go` | 新增端点 handler |
| `web/install/agent.sh` | 新增（嵌入用） |
| `web/install/agentscript.go` | 路由 + embed |
| `web/router/router.go` | 注册 `POST /api/clients/enroll` |
| `web/rpc/jsonrpc/admin.enroll.go` | 4 个管理 RPC |
| `cmd/app.go` | 孤儿回收 + 埋点回填 |
| `pkg/config/settings.go` | `enroll_enabled` 等配置键 |
| `database/clients/client.go` | enroll 创建节点的复用函数 |
| 测试 | `enroll_test.go`（幂等/过期/超限/网段/撤销）、`admin.enroll_test.go`、脚本解析测试 |

---

## 5. 验收标准

1. 同一 `ENROLL_KEY` 在 3 台不同机器执行 → 面板出现 **3 个独立节点**，各自在线、各自有流量
2. 同一台机器重复执行 2 次 → 仍是 **1 个节点**（幂等），token 不变
3. 第 N+1 次超过 `max_uses` → 返回 403，且**不新增节点**
4. 过期/已撤销的 key → 403
5. 超出 `allowed_cidrs` 的来源 IP → 403
6. 节点首次上报后，名称自动变为 `国家代码-IP-ASN-ISP`（沿用已有自动命名）
7. 注册后 60 分钟无上报的节点被清理，`UsedCount` 回滚
8. `enroll_enabled=false` 时端点 404，现有部署方式完全不受影响
9. `go build ./...`、`GOARCH=amd64 go vet ./...`、新增测试全绿

---

## 6. 交付步骤

1. 在 fork 上实现上述改动 + 测试（约 10 个文件）
2. 本地 `go build` / `go vet`（amd64）/ 定向测试
3. 本地起临时实例做**端到端**验证：`enroll_enabled=true` + 生成 key + 用脚本走一遍（在 VPS 的临时端口上，不碰正式实例）
4. push 到 fork → 触发 `build-fork-binary.yml` → 换 VPS 二进制
5. 在正式实例开启 `enroll_enabled`，生成一个 `MaxUses`/`ExpiresAt`/`AllowedCIDRs` 受限的 key
6. 拿真实子机跑一次，确认：节点独立、名称自动生成、WebSSH 可连

---

## 7. 与"方案 B/C"的对比（为什么选这个）

| | 本方案 | 方案 B（命令内嵌 api_key 换 token） | 方案 C（面板批量复制） |
| --- | --- | --- | --- |
| 同一条指令 | ✅ | ✅ | ❌ 每台一条 |
| 命令泄露的后果 | 可注册节点（受次数/时效/网段约束，可撤销） | **等于泄露管理员 API 权限** | 单台 token |
| 需改 agent | ❌ 不需要 | ❌ | ❌ |
| 需要 HTTPS | ✅ 建议强制 | ✅ | — |

---

## 8. 遗留风险与建议

- **必须上 HTTPS**：明文 key 与返回的 node token 都走这条通道。当前面板是 `http://67.215.228.4:27777`，
  建议先启用内置 HTTPS（36888）或前置反向代理，再把 enroll 端点对公网开放。
- 脚本从面板自身下发（不用 raw.githubusercontent），国内子机拉取更稳；如需 GitHub 代理，官方
  `install.sh` 仍支持 `--install-ghproxy`。
- `interval` 默认 600 秒；如需与面板实时性折中可改成 300 秒。
- `--enable-remote-control` 属安装边界，脚本里固定带上；漏掉会导致该节点远程功能只能重装才能开。
