# WebSSH-only 部署与节点自动命名

本文档描述面向「探针几乎只用于 WebSSH」场景的部署方式，包括一键部署默认参数、
节点自动命名，以及把主控占用压到最低所需的配置。

## 1. 一键部署的默认参数

新建节点无需任何额外配置，默认值已经面向 WebSSH 场景：

| 默认项 | 值 | 说明 |
| --- | --- | --- |
| 上报间隔 | **600 秒** | `client_default_interval_seconds`，可在设置中调整（1–3600） |
| 远程控制 | **开启** | 部署指令默认带 `--enable-remote-control` |
| 平台 | `linux` | 可在节点配置中改成 windows / macos / docker |

流程：

1. 后台「节点管理 → 添加节点」创建节点（无需填写名称）。
2. 打开该节点的「节点配置 → 部署指令」，复制完整命令。
3. 在目标服务器执行该命令；Agent 会自动带 **600 秒间隔 + 远程控制** 启动。
4. Agent 首次上报基础信息后，主控自动为节点命名（见下一节）。

> 已存在的节点保留各自已保存的部署配置，不会被新默认值改写。需要统一调整时，
> 用下面的批量脚本调用 `admin:saveClientDeploymentProfile`。

## 2. 节点自动命名

### 命名规则

```
国家代码-IP地址-ASN-ISP
例：CN-203.0.113.7-AS4837-China Unicom
```

- 国家代码取 ISO 3166-1 alpha-2 大写代码，例如 `CN`、`US`、`JP`。
- IP 地址优先使用 IPv4，未上报 IPv4 时使用 IPv6。
- ASN 去掉 `AS` 前缀后统一补回，例如上游返回 `AS4837` → 名称中为 `AS4837`。
- ISP 取运营商名称，内部空白折叠为单个空格。
- 任一部分缺失时自动省略，例如只解析到国家时得到 `JP-203.0.113.1`。

### 数据来源

默认向 `https://ipecho.5671234.xyz/?format=json` 查询，读取：

```json
{
  "ip": "203.0.113.7",
  "location": { "country": "CN", "countryName": "China" },
  "network": { "asn": "AS4837", "org": "China Unicom" }
}
```

同时兼容平铺字段（`country` / `country_code` / `asn` / `org` / `isp`）。
**上游无结果或请求失败时保留默认名称 `client_xxxxxxxx`**，不会写入空名称。

### 触发时机与性能

- 触发点是 Agent 的**基础信息上报**（`agent.basicInfo`），即节点第一次连上主控时。
- 上报路径**只做本地缓存查询，绝不发起网络请求**，因此上游故障不会拖慢 Agent 上报。
- 真正的探测由固定的 4 个后台工作协程执行，队列上限 4096；队列满时直接丢弃，
  下一次上报自动重试。
- 解析结果按 IP 缓存 6 小时；失败按 10 分钟负缓存，避免上游异常时被整批节点打爆。
- 同一节点最多尝试 3 次，失败重试间隔 30 分钟；超过限制后不再探测，保留默认名称。

### 管理员改名优先

- 只有名称仍是占位符 `client_xxxx`（或为空）时才会被自动命名。
- 你在后台手动改过的名称**永远不会被覆盖**。写入自动名称时要求当前名称仍是占位符，
  即使探测在改名之后才返回，也不会改动你的名称。
- 自动命名成功后写入 `name_auto_generated = true` 标记。

### 手动补齐历史节点

批量部署完成后，可以用一条指令补齐所有仍是占位符名称的节点：

```bash
# 补齐全部未命名节点
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:nameClients","params":{"all":true}}'

# 只处理指定节点
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:nameClients","params":{"uuids":["<uuid>"]}}'
```

返回 `named`（已命名列表）与 `skipped`（地址缺失或探测失败的节点数）。

### 相关配置键

| 键 | 默认值 | 说明 |
| --- | --- | --- |
| `client_naming_enabled` | `true` | 关闭后不再自动命名，上报路径完全不做命名判断 |
| `client_naming_echo_url` | `https://ipecho.5671234.xyz/?format=json` | 出口探测地址，可换自建服务 |
| `client_naming_timeout_seconds` | `5` | 单次探测超时（最大 60） |
| `client_default_interval_seconds` | `600` | 新节点的上报间隔，同时决定内存中近期上报窗口 |

写入方式（局部更新设置）：

```bash
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:editSettings","params":{
        "client_naming_enabled": true,
        "client_naming_echo_url": "https://ipecho.5671234.xyz/?format=json",
        "client_default_interval_seconds": 600 }}'
```

## 3. 把主控占用压到最低

WebSSH 只依赖「连接 + 在线状态」，不依赖监控数据。把遥测关掉即可获得最大收益。

### 3.1 关闭指标落盘

把每个指标定义的保留期设为 0：

```bash
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:listMetricDefinitions","params":{}}'

# 对返回的每个 name 执行：
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:updateMetricDefinition","params":{"name":"cpu.usage","retention_days":0}}'
```

保留期为 0 时：

- 上报的点在落库前被丢弃，磁盘写入归零；
- 全部指标都为 0 后，写入路径直接短路，不再做逐点过滤、流量累计与点位构造；
- 上报仍返回成功，面板继续显示「最近一次」数值，在线状态与 WebSSH 不受影响。

### 3.2 关闭无关后台功能

```bash
curl -sS -X POST "$BASE/api/rpc2" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:editSettings","params":{
        "notification_enabled": false,
        "notification_method": "none",
        "geo_ip_enabled": false,
        "visitor_audit_enabled": false,
        "allow_mcp": false,
        "traffic_limit_percentage": 0,
        "allow_remote_management": true}}'
```

`allow_remote_management` 必须保持 `true`，否则 WebSSH 不可用。

不要在节点上创建 Ping 任务与回程线路任务。

### 3.3 预期效果（10000 节点）

| 指标 | 默认监控 | WebSSH-only |
| --- | --- | --- |
| 上报吞吐需求 | 3333/s（队列上限不足，丢数据） | 约 16.7/s（600 秒间隔） |
| 指标落盘 | 持续写入并压缩 | 0 |
| 磁盘稳态 | 数十 GB | 约 0.3–0.6 GB（仅 `lite.db`） |
| 主控内存 | 1.2–3 GB | 约 0.35–0.7 GB |

### 3.4 本次内置的性能改动

| 位置 | 改动 | 作用 |
| --- | --- | --- |
| `database/metricstore/report.go` | 批量刷盘间隔 3s → 1s，队列 256 → 8192 | 写入吞吐上限 85/s → 8192/s |
| `web/agent/report_window.go` | 内存近期上报窗口跟随上报间隔（10s–20min） | 长间隔节点不再常驻一分钟的样本 |
| `web/api/WebSocket.go` | Agent 侧 WebSocket 读写缓冲 4096 → 1024 字节 | 每连接省约 6 KiB，万级节点省约 60 MiB |
| `pkg/metric/store.go` | 全部指标关闭时写入路径短路 | 遥测关闭后写入零开销 |

## 4. 验证清单

```bash
# 指标保留期是否都为 0
curl -sS -X POST "$BASE/api/rpc2" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"admin:listMetricDefinitions","params":{}}'

# 新节点部署配置是否为 600 秒且已开远程控制
curl -sS -X POST "$BASE/api/rpc2" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"admin:getClientDeploymentProfile","params":{"uuid":"<uuid>"}}'

# 节点名称是否已生成
curl -sS -X POST "$BASE/api/rpc2" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"admin:listClients","params":{}}'

# 磁盘是否停止增长（间隔 10 分钟观察两次）
ls -l ./data/metrics.db
```

成功标志：`metrics.db` 大小不变、进程内存稳定、WebSSH 可正常打开终端、
新节点名称为 `国家代码-IP-ASN-ISP` 形式。

## 5. 注意事项

- 关闭指标保留期会丢失历史曲线与趋势图，流量累计账本仍保留。
- 自动命名需要主控能访问出口探测服务；内网隔离环境请自行部署探测服务并改
  `client_naming_echo_url`。
- 已存在且名称不是占位符的节点不会被改名；需要重新命名时先手动改回
  `client_xxx` 形式，或使用 `admin:nameClients` 指定 UUID（该接口同样只处理占位符名称）。
