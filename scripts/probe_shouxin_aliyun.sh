#!/usr/bin/env bash
# 阿里云 ECS：守信三条新路由 (dtjd / snhmd / dtly) 上游连通性一键探测。
#
# 从 CONFIG_FILE 读取 institutionId / aesKey / licenseFile / oss（与 relay 同源），
# 直连 https://shouwei.shouxin168.com/... 各测一次；不写任何密钥到脚本里。
#
# 前置条件（在仓库根目录执行）：
#   - 已安装 Go（与部署 relay 相同环境即可）
#   - config.aliyun.prod.yaml 中三条路由已填 institutionId、aesKey
#   - 本机出口 IP 已加入守信白名单（测试环境同样要求）
#   - 测试环境：不配 licenseFile/oss 即不传授权书（上游口径，licenseUrl 送空串）
#   - 生产环境：必须配 licenseFile + oss（原文 licenseUrl 必传），授权书文件在 ECS 上真实存在
#
# 用法：
#   chmod +x scripts/probe_shouxin_aliyun.sh
#   ./scripts/probe_shouxin_aliyun.sh
#   CONFIG_FILE=/path/to/config.yaml ./scripts/probe_shouxin_aliyun.sh
#
# 覆盖探测三要素（默认：陈韫 / 440303200002163115 / 13670010670）：
#   PROBE_NAME=... PROBE_IDCARD=... PROBE_MOBILE=... ./scripts/probe_shouxin_aliyun.sh
#
# 【凭证还没拿到时】只验证网络可达与出口 IP 是否已加白（不做业务调用）：
#   PROBE_NET_ONLY=1 ./scripts/probe_shouxin_aliyun.sh
#   上游文档 §2.3 规定 institution_id 必传，没有真值就无法发起真实业务查询。
#
# 成功：每条路由归一化码 001（查得）或 999（查无）即 PASS；任一条 FAIL 则整脚本 exit 1。

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

CONFIG_FILE="${CONFIG_FILE:-config.aliyun.prod.yaml}"
export CONFIG_FILE
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

# 默认联调样本（可用环境变量覆盖）
export PROBE_NAME="${PROBE_NAME:-陈韫}"
export PROBE_IDCARD="${PROBE_IDCARD:-440303200002163115}"
export PROBE_MOBILE="${PROBE_MOBILE:-13670010670}"

COMMON="./scripts/probe_shouxin_common.go"

log() { echo "[$(date '+%F %T')] $*"; }
fail() { log "错误: $*" >&2; exit 1; }

[ -f "$CONFIG_FILE" ] || fail "配置文件不存在: $REPO_DIR/$CONFIG_FILE"
[ -f "$COMMON" ] || fail "缺少 $COMMON（请上传完整 DataHub 仓库或至少 scripts 探测文件）"
command -v go >/dev/null 2>&1 || fail "未找到 go，请先安装 Go 或使用与 relay 相同的 PATH"

log "DataHub 守信三路由上游探测"
log "  工作目录: $REPO_DIR"
log "  配置:     $CONFIG_FILE"
if [ "${PROBE_NET_ONLY:-}" = "1" ]; then
  export PROBE_NET_ONLY
  log "  模式:     仅网络连通性（不做业务调用，无需真实凭证）"
else
  log "  样本:     name=$PROBE_NAME idCard=$PROBE_IDCARD mobile=$PROBE_MOBILE"
fi
echo ""

PASS=0
FAIL=0

run_probe() {
  local route="$1"
  local main_go="./scripts/probe_${route}.go"
  [ -f "$main_go" ] || fail "缺少 $main_go"

  echo "────────────────────────────────────────"
  log "开始: $route"
  if go run "$main_go" "$COMMON"; then
    PASS=$((PASS + 1))
    log "$route: PASS"
  else
    FAIL=$((FAIL + 1))
    log "$route: FAIL"
  fi
  echo ""
}

run_probe dtjd
run_probe snhmd
run_probe dtly

echo "════════════════════════════════════════"
log "汇总: PASS=$PASS FAIL=$FAIL (共 3 条)"
if [ "$FAIL" -gt 0 ]; then
  log "至少一条未通过 → 检查 institutionId/aesKey、授权书、OSS、IP 白名单、mode 是否抄错"
  exit 1
fi
log "全部通过"
exit 0
