#!/bin/sh
# Lite 探针批量部署脚本（由主控下发）。
#
# 同一条指令可在任意多台机器执行：脚本会向主控领取**本机专属**的节点身份，
# 写入 agent 配置文件，再调用官方 install.sh 完成安装。
# 重复执行是幂等的：主控按机器指纹返回同一个节点与 token。
#
# 用法（由主控生成，通常无需手写）：
#   curl -fsSL https://<面板域名>/install/agent.sh | sudo bash -s -- \
#     -e https://<面板域名> -k <ENROLL_KEY> [-i 600] [--install-ghproxy URL]

set -eu

ENDPOINT=""
ENROLL_KEY=""
INTERVAL=""
AGENT_DIR="/opt/lite-agent"
CONFIG_PATH=""
GHPROXY=""
INSTALL_URL="https://raw.githubusercontent.com/nuomiiiii/Lite-agent/main/install.sh"
INSTALL_VERSION=""

log_info() { printf '%s\n' "$*"; }
log_err() { printf '%s\n' "$*" >&2; }

usage() {
    cat >&2 <<'USAGE'
Usage: install via Lite panel:
  curl -fsSL https://<panel>/install/agent.sh | sudo bash -s -- \
    -e <endpoint> -k <enroll-key> [options]

Options:
  -e, --endpoint URL     Lite panel base URL (required)
  -k, --enroll-key KEY   Enrollment key issued by the panel (required)
  -i, --interval SEC     Report interval in seconds (default 600)
      --dir PATH         Agent install directory (default /opt/lite-agent)
      --config PATH      Agent config file path (default <dir>/config.json)
      --install-ghproxy URL  GitHub proxy for the official installer
      --install-version VER  Pin the agent version
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        -e|--endpoint) ENDPOINT="${2:-}"; shift 2 ;;
        --endpoint=*) ENDPOINT="${1#--endpoint=}"; shift ;;
        -k|--enroll-key) ENROLL_KEY="${2:-}"; shift 2 ;;
        --enroll-key=*) ENROLL_KEY="${1#--enroll-key=}"; shift ;;
        -i|--interval) INTERVAL="${2:-}"; shift 2 ;;
        --interval=*) INTERVAL="${1#--interval=}"; shift ;;
        --dir) AGENT_DIR="${2:-}"; shift 2 ;;
        --config) CONFIG_PATH="${2:-}"; shift 2 ;;
        --install-ghproxy) GHPROXY="${2:-}"; shift 2 ;;
        --install-version) INSTALL_VERSION="${2:-}"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) log_err "unknown argument: $1"; usage; exit 1 ;;
    esac
done

if [ -z "$ENDPOINT" ] || [ -z "$ENROLL_KEY" ]; then
    log_err "both -e/--endpoint and -k/--enroll-key are required"
    usage
    exit 1
fi
if [ "$(id -u)" != "0" ]; then
    log_err "please run as root (use sudo)"
    exit 1
fi

# 去掉结尾斜杠，避免拼出 //api/...
ENDPOINT="${ENDPOINT%/}"

case "$INTERVAL" in
    '') INTERVAL="600" ;;
esac
if ! printf '%s' "$INTERVAL" | grep -qE '^[0-9]+$' || [ "$INTERVAL" -lt 1 ] || [ "$INTERVAL" -gt 3600 ]; then
    log_err "interval must be an integer between 1 and 3600 (got: $INTERVAL)"
    exit 1
fi

if [ -z "$CONFIG_PATH" ]; then
    CONFIG_PATH="$AGENT_DIR/config.json"
fi

# 依赖：curl 与 tar/python 皆为可选，优先 curl
if ! command -v curl >/dev/null 2>&1; then
    log_err "curl is required"
    exit 1
fi

# ---------------------------------------------------------------------------
# 1) 机器指纹：优先 /etc/machine-id，回退到 hostname + 首个 MAC
# ---------------------------------------------------------------------------
fingerprint_source=""
if [ -r /etc/machine-id ]; then
    fingerprint_source="$(cat /etc/machine-id)"
elif [ -r /var/lib/dbus/machine-id ]; then
    fingerprint_source="$(cat /var/lib/dbus/machine-id)"
fi
if [ -z "$fingerprint_source" ]; then
    mac=""
    for iface in /sys/class/net/*/address; do
        [ -r "$iface" ] || continue
        addr="$(cat "$iface" 2>/dev/null || true)"
        case "$addr" in
            ""|"00:00:00:00:00:00") continue ;;
        esac
        mac="$addr"
        break
    done
    fingerprint_source="$(hostname 2>/dev/null || echo unknown)-$mac"
fi

if command -v sha256sum >/dev/null 2>&1; then
    FINGERPRINT="$(printf '%s' "$fingerprint_source" | sha256sum | cut -d' ' -f1)"
elif command -v shasum >/dev/null 2>&1; then
    FINGERPRINT="$(printf '%s' "$fingerprint_source" | shasum -a 256 | cut -d' ' -f1)"
else
    # 最后兜底：确定性但可读的标识，仍可让主控按机器区分
    FINGERPRINT="$(printf '%s' "$fingerprint_source" | od -An -tx1 | tr -d ' \n')"
fi

HOSTNAME_VALUE="$(hostname 2>/dev/null || echo unknown)"

log_info "[1/4] 向主控注册本机身份（$ENDPOINT）"
PAYLOAD="{\"fingerprint\":\"$FINGERPRINT\",\"hostname\":\"$HOSTNAME_VALUE\"}"

RESPONSE="$(curl -fsS -m 30 -X POST "$ENDPOINT/api/clients/enroll" \
    -H "Authorization: Bearer $ENROLL_KEY" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -d "$PAYLOAD" 2>/dev/null || true)"

if [ -z "$RESPONSE" ]; then
    log_err "注册失败：主控无响应或拒绝了请求。请检查 enroll 密钥、来源 IP 白名单与主控日志。"
    exit 1
fi

# 从响应中提取 token / uuid：优先 python3，回退 sed
extract_field() {
    field="$1"
    if command -v python3 >/dev/null 2>&1; then
        printf '%s' "$RESPONSE" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
except Exception:
    sys.exit(1)
v=(d.get('data') or {}).get('$field')
if not isinstance(v,str) or not v:
    sys.exit(1)
sys.stdout.write(v)
" 2>/dev/null && return 0
    fi
    printf '%s' "$RESPONSE" | sed -n "s/.*\"$field\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n1
}

TOKEN="$(extract_field token || true)"
NODE_UUID="$(extract_field uuid || true)"

if [ -z "$TOKEN" ]; then
    log_err "注册响应里没有 token：$RESPONSE"
    exit 1
fi
log_info "      节点已登记: ${NODE_UUID:-unknown}"

# ---------------------------------------------------------------------------
# 2) 写 agent 配置文件
# ---------------------------------------------------------------------------
log_info "[2/4] 写 agent 配置 $CONFIG_PATH"
mkdir -p "$AGENT_DIR"
chmod 700 "$AGENT_DIR" 2>/dev/null || true

cat > "$CONFIG_PATH" <<JSON
{
  "endpoint": "$ENDPOINT",
  "token": "$TOKEN",
  "interval": $INTERVAL,
  "remote_control_enabled": true,
  "disable_auto_update": false
}
JSON
chmod 600 "$CONFIG_PATH"

# ---------------------------------------------------------------------------
# 3) 调用官方安装脚本（带 --config，服务单元会复用该参数）
# ---------------------------------------------------------------------------
log_info "[3/4] 安装 agent（官方 install.sh）"
INSTALLER="$(mktemp)"
trap 'rm -f "$INSTALLER"' EXIT

DOWNLOAD_URL="$INSTALL_URL"
if [ -n "$GHPROXY" ]; then
    DOWNLOAD_URL="${GHPROXY%/}/$INSTALL_URL"
fi
if ! curl -fsSL -m 60 -o "$INSTALLER" "$DOWNLOAD_URL"; then
    log_err "下载 install.sh 失败: $DOWNLOAD_URL"
    exit 1
fi
if [ ! -s "$INSTALLER" ]; then
    log_err "下载到的 install.sh 为空"
    exit 1
fi

set -- --config "$CONFIG_PATH" --enable-remote-control
if [ -n "$GHPROXY" ]; then
    set -- "$@" --install-ghproxy "$GHPROXY"
fi
if [ -n "$INSTALL_VERSION" ]; then
    set -- "$@" --install-version "$INSTALL_VERSION"
fi

if command -v bash >/dev/null 2>&1; then
    bash "$INSTALLER" "$@"
else
    sh "$INSTALLER" "$@"
fi

# ---------------------------------------------------------------------------
# 4) 结果
# ---------------------------------------------------------------------------
log_info "[4/4] 完成"
log_info "      节点 UUID : ${NODE_UUID:-unknown}"
log_info "      配置      : $CONFIG_PATH"
log_info "      采集间隔  : ${INTERVAL}s"
log_info "      远程控制  : 已开启"
log_info "      稍后打开面板，该节点名称会自动变为「国家代码-IP-ASN-ISP」"
