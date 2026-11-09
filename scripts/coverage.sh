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

# 行内豁免（cov:excl）——结构性不可测语句的显式标记：
#   某些手写语句在**正确的**测试下也不可达——内嵌 fs 读取恒成功、crypto/rand 与
#   x509 签名对合法输入不报错、MarshalIndent 纯 map 不失败、以及 root CI 跑时
#   无法用 chmod 触发的 MkdirAll/WriteFile/Stat 权限错误（本地能造、CI 造不出＝假绿）。
#   在这些语句所在行加 `//cov:excl <理由>` 即从分子与分母**同时剔除**——不是刷绿，
#   而是把「无法测」显式化、可审计（业界同款：coverage.py 的 pragma: no cover）。
#   纪律：哨兵后必须带理由，否则视为裸豁免、闸门拒绝。
if grep -rn --include='*.go' '[[:space:]]//cov:excl[[:space:]]*$' . 2>/dev/null; then
  echo "coverage: 上面这些 //cov:excl 缺理由（哨兵后必须写原因）" >&2
  exit 1
fi

# 合并 + 去生成物/入口 + 逐包聚合。
#
# 并集语义（关键）：同一 block（file:行范围）可能同时出现在 short 腿与 integration 腿
# 的 profile 里——必须按 block 取「命中次数的最大值」（任一腿命中即算覆盖），而不是把
# 语句数累加。naive `sum` 会让同时出现在两腿的包分母翻倍、覆盖率失真。disjoint 模块
# （每包只在一腿）下 max==once，与旧求和逐字节等价，故本地全量基线数字不变。
cat "$PDIR"/*.out 2>/dev/null | awk -v gate="$GATE" '
/^mode:/ {next}
{
  if (NF<3) next
  loc=$1; numstmt=$2+0; cnt=$3+0
  if (!((loc) in bcnt) || cnt > bcnt[loc]) { bcnt[loc]=cnt; bnum[loc]=numstmt }
}
# is_excl：把 import-path 文件映射回 ROOT 相对路径（所有模块路径都以
# github.com/kalandramo/bald/ 开头，剥前缀即磁盘路径），按文件缓存行内容，
# block 覆盖的行范围内出现 cov:excl 即判豁免。
function is_excl(file, sl, el,   rel, i, n, line) {
  rel = file
  sub(/^github\.com\/kalandramo\/bald\//, "", rel)
  if (!(rel in lcount)) {
    n = 0
    while ((getline line < rel) > 0) { lbuf[rel, n + 1] = line; n++ }
    close(rel)
    lcount[rel] = n
  }
  n = lcount[rel]
  for (i = sl; i <= el && i <= n; i++) {
    if (lbuf[rel, i] ~ /cov:excl/) return 1
  }
  return 0
}
END {
  printf "%-64s %8s %8s %7s\n", "package", "stmts", "cov", "%"
  for (loc in bcnt) {
    n=split(loc, a, ":"); if (n<2) continue
    file=a[1]
    base=file; sub(/.*\//,"",base)
    if (base ~ /\.pb\.go$/ || base ~ /\.pb\.gw\.go$/ || base ~ /_grpc\.pb\.go$/ ||
        base ~ /\.pb\.validate\.go$/ || base ~ /\.gen\.go$/ || base=="main.go" ||
        file ~ /\/mocks?\//) continue
    split(a[2], r2, ",")
    sl=r2[1]; sub(/\..*/,"",sl); sl+=0
    el=r2[2]; sub(/\..*/,"",el); el+=0
    if (is_excl(file, sl, el)) continue
    pkg=file; sub(/\/[^\/]*\.go$/, "", pkg)
    T[pkg]+=bnum[loc]; if (bcnt[loc]>0) C[pkg]+=bnum[loc]
    GT+=bnum[loc]; if (bcnt[loc]>0) GC+=bnum[loc]
  }
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
