#!/usr/bin/env bash
# 声明式装配验收闸门（风险登记册 §8 最后半条「acceptance 没有任何 CI 作业调用它」的收口）。
#
# ## 为什么不能把 `go test -run <正则>` 直接接进 CI
#
# `-run` 匹配到 0 个测试时**返回 0**：用例被改名、被删、正则写错，作业照样全绿。
# 本仓库已经被这类静默失效咬过两次（§7.1 的「不可达即 Skip」、§8 的 per-command
# `dir` 被 go-task 忽略），所以这里把「清单」与「执行」钉在一起：
#   1. `go test -list <正则>` 取出这一腿**当前真实命中**的顶层测试名；命中为空即红；
#   2. `go test -v -count=1` 跑同一正则，逐条断言日志里出现 `^--- PASS: <名字> (`；
#      Skip、改名、没链上都会落到这里。
# 判据来自 `-list` 而不是手写清单，因此增删用例不需要维护第二份真相；口径与
# ci.yml 的 integration 作业同一条。
#
# ## scope
#
#   all（默认，本地）：全部腿，含 `_example/bald`。
#   ci              ：只跑标 ci 的腿。两条 `no` 的各自理由：
#                   - `_example/bald` 的参考工程计划要编译 `api/gen` 的 proto
#                     产物，而它已 gitignore（同 §7.1「刻意不含 `_example`」），
#                     干净检出装载不了；
#                   - `bconf` 的 `TestGeneratedMatchesProto` 判的是「本机 buf +
#                     protoc-gen-go 重出的产物与入库字节一致」，**这个字节判据绑
#                     工具链**：官方发行版 buf 1.50.0 复现零差异，1.57.2 会给中文注释的 `//`
#                     补一个空格（`bootstrap/v1/log.pb.go` 实测）；同一插件版本号
#                     在 BSR 容器里跑也与本机二进制差这一处空格。版本 pin 现在成文
#                     了（`bconf/buf.version` + 产物头部的 protoc-gen-go 版本行，
#                     两条都在字节比对之前单独判红），但 CI 仍不该为此装 buf——
#                     pin 还钉不住来源：同号的 `go install` 源码构建与官方发行
#                     二进制产出不同（版本门过、字节门红），见 §7.9 第三轮。
#                     装什么版本都是「用工具链版本换一条绿」，见登记册 §7.9。
#
# 表列以 TAB 分隔，**tags 放最后**（TAB 属 IFS 空白，连续 TAB 会被折叠成一个分隔符，
# 中间列留空就会串位；可空列只能放在行尾）。
#   <ci|no>	<模块目录>	<包>	<-run 正则>	<go build tags，可空>
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export GOWORK=off

scope=${1:-all}
case "$scope" in
all | ci) ;;
*)
  echo "usage: $0 [all|ci]" >&2
  exit 2
  ;;
esac

rows() {
  printf '%s\n' \
    $'ci\tpkg\t./appkit\tTestPlan_(NoSideEffects|OrderInvariant|SegmentCoverage|Conflict|DomainRoutes|CustomRoutesWarning)|TestRouteShapeConflict\t' \
    $'ci\tpkg\t./appkit\tTestTeardownOrder_|TestProviderPlan_StdTeardownOrder|TestComponent_StartFailureRollback|TestMountComponent_MountFailureRollback\t' \
    $'ci\tpkg\t./appkit\tTestDrift\t' \
    $'ci\tpkg\t./appkit\tTestPlanCache_|TestVariantProjection_|TestVariantEffects_|TestOption_OnRoleScoping|TestDoctor_\t' \
    $'ci\tpkg\t./appkit\tTestAPI_\t' \
    $'ci\tcli\t./internal/codegen\tTestGen_(Deterministic|CheckClean)|TestGenDomain_(Bind|Proto|NoRoutes|CheckDoesNotRunProtoStage)|TestGenApp_Variants|TestTree_Variants|TestTree_CustomWiring|TestPruneStaleOwned|TestCheckTree_\t' \
    $'no\tbconf\t.\tTestGeneratedMatchesProto\t' \
    $'no\t_example/bald\t./internal/bootstrap\tTestPlan_ReferenceProject\t' \
    $'no\t_example/bald\t./internal/bootstrap\tTestPlan_ReferenceProject\tgrpcgw'
}

fail=0
ran=0
checks=0
while IFS=$(printf '\t') read -r in_ci dir pkg regex tags; do
  [ -n "${in_ci:-}" ] || continue
  if [ "$scope" = ci ] && [ "$in_ci" != ci ]; then
    echo "SKIP（scope=ci 不含这条腿，理由见表头）: $dir $pkg $regex"
    continue
  fi
  ran=$((ran + 1))
  echo "=== acceptance: $dir $pkg${tags:+ -tags $tags} -run $regex"

  # bash 3.2（macOS 默认）在 set -u 下展开空数组会报 unbound，故用 ${arr[@]+...}。
  tagargs=()
  [ -n "${tags:-}" ] && tagargs=(-tags "$tags")

  log=$(mktemp)
  # 1) 清单：正则当前命中哪些顶层测试。空清单 = 名字漂了，闸门必须响。
  if ! (cd "$dir" && go test -list "$regex" ${tagargs[@]+"${tagargs[@]}"} "$pkg") >"$log" 2>&1; then
    echo "::error::go test -list 失败（$dir $pkg）"
    cat "$log"
    fail=$((fail + 1))
    rm -f "$log"
    continue
  fi
  names=$(grep -E '^Test[A-Za-z0-9_]+$' "$log" | sort -u || true)
  rm -f "$log"
  if [ -z "$names" ]; then
    echo "::error::-run 正则在 $dir $pkg 命中 0 个测试——用例改名或被删时 -run 照样返回 0，这正是本闸门存在的理由"
    fail=$((fail + 1))
    continue
  fi
  echo "    命中：${names//$'\n'/ }"

  # 2) 执行并逐条验活。
  log=$(mktemp)
  if ! (cd "$dir" && go test -count=1 -timeout 10m -v -run "$regex" ${tagargs[@]+"${tagargs[@]}"} "$pkg") >"$log" 2>&1; then
    echo "::error::测试腿失败（$dir $pkg）"
    # 打原文而不是挑行：t.Fatalf 的清单缩进在两空格，按 `^--- ` 过滤会把根因整段吞掉。
    tail -40 "$log"
    fail=$((fail + 1))
    rm -f "$log"
    continue
  fi
  for name in $names; do
    checks=$((checks + 1))
    if grep -qE "^--- PASS: ${name} \(" "$log"; then
      echo "    PASS $name"
    else
      echo "::error::判据 $name 没有顶层 PASS 记录（Skip / 改名 / 未编译都会落到这里）"
      grep -E "^(    )?(--- )?(SKIP|FAIL): ${name}" "$log" | head -5
      fail=$((fail + 1))
    fi
  done
  rm -f "$log"
done <<EOF
$(rows)
EOF

if [ "$ran" = 0 ]; then
  echo "::error::一腿都没跑（scope=$scope，表为空或谓词被改坏）——闸门静默失效"
  exit 1
fi
if [ "$fail" != 0 ]; then
  echo "acceptance-gate: $ran 条腿 / $checks 条判据，$fail 条未通过"
  exit 1
fi
echo "acceptance-gate: scope=$scope 的 $ran 条腿、$checks 条判据全部通过"
