#!/usr/bin/env bash
# bald 覆盖率测量/闸门。
#
#   scripts/coverage.sh                 # 打印聚合覆盖率 + 每包缺口（reporting，不判失败）
#   scripts/coverage.sh --gate 100      # 任一「纳入统计」的包 <100% 即非零退出（CI 阻断用）
#   scripts/coverage.sh --integration   # 追加非 -short 用例（需真后端 etcd/redis/...，本地一般跳过）
#   scripts/coverage.sh --merge DIR     # 不跑测试，只合并 DIR 下已有 *.out（CI 腿并集用）
#
# --merge 的用途：CI 各集成腿（etcd/redis/consul/kafka/mongo/...）在带真后端时才能覆盖
#   基础设施强相关包，且这些腿分属不同 job。每条腿各自 `-coverprofile=.../<leg>.out`
#   落同一 artifact 目录，最后一个 job 用 `--merge DIR --gate N` 求「全腿并集」再判闸门。
#   单本地 run 无法产并集（无服务，多数集成用例 Skip），故「100%」的口径必须是 CI 并集。
#
# 口径（可复现，且与「100% 现实可达」一致）：
#   - 统计范围 = 手写代码的语句覆盖；**排除**生成物与入口：
#       *.pb.go / *.pb.gw.go / *_grpc.pb.go / *.pb.validate.go / *.gen.go（buf/bald 生成，
#       不可达分支多，纳入即数学上不可能 100%）
#       main.go（cmd 入口桩）
#       /mock(s)/ 目录
#   - 每模块跑 `go test -short`（与 CI test 作业同范围）；集成腿在 --integration 时并入。
#   - 逐包（package）算覆盖，不做「混合总数」——混合 100% 会因个别包拉低而失真，
#     真正的 100% 要求「每个纳入包都 100%」。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GATE=""
SHORT="-short"
MERGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --gate) GATE="${2:?--gate 需要百分比数字}"; shift 2 ;;
    --integration) SHORT=""; shift ;;
    --merge) MERGE="${2:?--merge 需要 profile 目录}"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

if [ -n "$MERGE" ]; then
  # 合并模式：profile 由 CI 各腿写入，本脚本不跑测试。
  PDIR="$MERGE"
  # 清单为空 = 闸门静默失效，必须响（与 list-modules.sh 同纪律）。
  ls "$PDIR"/*.out >/dev/null 2>&1 || { echo "coverage: $PDIR 下无 *.out profile，拒绝空判" >&2; exit 1; }
else
  PDIR="$(mktemp -d /tmp/bald-cov.XXXXXX)"
  trap 'rm -rf "$PDIR"' EXIT
  i=0
  while IFS= read -r m; do
    [ -z "$m" ] && continue
    d="$ROOT"; [ "$m" != "." ] && d="$ROOT/$m"
    [ -f "$d/go.mod" ] || continue
    i=$((i+1))
    prof="$PDIR/m$i.out"
    ( cd "$d" && GOWORK=off GOFLAGS=-mod=mod \
        go test $SHORT -timeout 320s -covermode=atomic -coverprofile="$prof" ./... >/dev/null 2>&1 ) || true
  done < <(bash scripts/list-modules.sh)
  ls "$PDIR"/*.out >/dev/null 2>&1 || { echo "coverage: 未产出任何 profile，拒绝空判" >&2; exit 1; }
fi

# 合并 + 去生成物/入口 + 逐包聚合。
cat "$PDIR"/*.out 2>/dev/null | awk -v gate="$GATE" '
/^mode:/ {next}
{
  if (NF<3) next
  loc=$1; stmts=$2+0; cnt=$3+0
  split(loc, p, ":"); file=p[1]
  base=file; sub(/.*\//,"",base)
  if (base ~ /\.pb\.go$/ || base ~ /\.pb\.gw\.go$/ || base ~ /_grpc\.pb\.go$/ ||
      base ~ /\.pb\.validate\.go$/ || base ~ /\.gen\.go$/ || base=="main.go" ||
      file ~ /\/mocks?\//) next
  pkg=file; sub(/\/[^\/]*\.go$/, "", pkg)
  T[pkg]+=stmts; if (cnt>0) C[pkg]+=stmts
  GT+=stmts; if (cnt>0) GC+=stmts
}
END {
  printf "%-64s %8s %8s %7s\n", "package", "stmts", "cov", "%"
  for (k in T) {
    pc=(T[k]?C[k]*100/T[k]:100)
    printf "%-64s %8d %8d %6.1f%%\n", k, T[k], C[k], pc
  }
  printf "\nAGGREGATE(hand-written, excl. generated/main): %d/%d = %.2f%%\n", GC, GT, (GT?GC*100/GT:0)
  if (gate!="") {
    bad=0
    for (k in T){ pc=(T[k]?C[k]*100/T[k]:100); if (pc < gate+0){ bad++; printf "BELOW %s%%: %s (%.1f%%)\n", gate, k, pc } }
    if (bad>0){ printf "\nGATE FAIL: %d packages below %s%%\n", bad, gate; exit 1 }
    printf "\nGATE PASS: all packages >= %s%%\n", gate
  }
}'
