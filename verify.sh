#!/usr/bin/env bash
# annotos 连通性验证脚本 —— 在【有专线】的节点上执行（如 192.0.2.1）
# 用法: bash verify.sh [bucket]
set -u
BUCKET="${1:-example-bucket}"
BIN="$(cd "$(dirname "$0")" && pwd)/annotos"

echo "===== annotos 验证: $(hostname) $(date '+%F %T') ====="
[ -x "$BIN" ] || { echo "❌ 找不到 $BIN，请把 annotos 二进制和 annotos.json 放到同一目录"; exit 1; }
[ -f "$(dirname "$BIN")/annotos.json" ] || { echo "❌ 缺少 annotos.json"; exit 1; }

"$BIN" version
echo
echo "----- 1. check: 连接与权限诊断 -----"
"$BIN" check || echo "⚠️ check 未通过"
echo
echo "----- 2. ls: 列出 bucket 根目录 -----"
"$BIN" ls "tos://$BUCKET/" || echo "⚠️ ls 失败"
echo
echo "===== 完成 ====="
