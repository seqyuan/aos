#!/usr/bin/env bash
# aos 连通性验证脚本 —— 在【有专线】的节点上执行
# 用法: bash verify.sh [bucket]
set -u
BUCKET="${1:-example-bucket}"
BIN="$(cd "$(dirname "$0")" && pwd)/aos"

echo "===== aos 验证: $(hostname) $(date '+%F %T') ====="
[ -x "$BIN" ] || { echo "❌ 找不到 $BIN，请把 aos 二进制和 aos.json 放到同一目录"; exit 1; }
[ -f "$(dirname "$BIN")/aos.json" ] || { echo "❌ 缺少 aos.json"; exit 1; }

"$BIN" version
echo
echo "----- 1. check: 连接与权限诊断 -----"
"$BIN" check || echo "⚠️ check 未通过"
echo
echo "----- 2. ls: 列出 bucket 根目录 -----"
"$BIN" ls "tos://$BUCKET/" || echo "⚠️ ls 失败"
echo
echo "===== 完成 ====="
