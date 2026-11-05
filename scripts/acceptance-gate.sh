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
#                   - `_example/bald` 的两条计划腿要编译 `api/gen` 的 proto 产物，
#                     而它已 gitignore——干净检出里连示例 module 都编译不了，本脚本
#                     不装 buf（装一遍工具链是专职作业的活）。**这不等于「这条腿没有
#                     远端证据」**：ci.yml 的 `example` 作业按同一把 pin 的 buf
#                     （scripts/install-buf.sh）生成产物后跑三态构建，两条
#                     `TestPlan_ReferenceProject`（默认态 + grpcgw 态）都在它的判据
#                     清单里逐条断言顶层 `--- PASS`（登记册 §7.1）。留 `no` 是为了
#                     不在两条作业里各装一遍 buf。
#                   - `bconf` 的 `TestGeneratedMatchesProto` 判的是「buf +
#                     protoc-gen-go 重出的产物与入库字节一致」，**这个字节判据绑
#                     工具链**：进字节的排版变量是**编译 protoc-gen-go 用的那把 Go**
#                     （go1.26.5 给中文注释的 `//` 补一个空格，go1.27.1/go1.27.3 与
#                     入库零差异；BSR 容器里的同名插件补空格也是同一成因），buf 的
#                     版本号实测对这一处中性（1.50.0 与 1.57.2 配同一把插件都零差
#                     异）。三把 pin 都成文并机器判了：`bconf/buf.version`（这条腿
#                     唯一还读 PATH 的一轴）、插件 module 版本比 `bconf/go.mod` 的
#                     protobuf **现编**出的那把（再比产物头部的 protoc-gen-go 版本行）、
#                     编译该插件的 Go major.minor 比 `bconf/protoc-gen-go.goversion`。
#                     后两轴读 PATH 上已装插件时，「那把是哪年装的」能冒充判据通过或
#                     判红（登记册 §7.9 前四轮），现编之后 PATH 里那把不再参与。
#                     本作业**不装 buf**：它的工具链前提与 `_example` 那两条不同，
#                     已经由专职作业 `proto-contract` 满足并取证（官方 buf 发行
#                     二进制按 `bconf/buf.sha256` 校验 + 判据自己现编插件 +
#                     「没有顶层 PASS 即红」），见登记册 §7.9 第五/六轮。本表留 `no`
#                     是为了不在两条作业里各装一遍工具链——不是「这条腿没有远端证据」。
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

# transport/asynq（§7.19）与 contrib/database/gorm（§7.20）两条腿的后端都在**进程内**
# ——前者是 miniredis，后者是纯 Go 的 go_sqlite 驱动落 t.TempDir() 的文件——都不需要
# 服务容器，所以它们能直接落进 scope=ci。与 nacos/consul 那类要真服务的腿不同。
# registry/nacos 那条（§7.22）同理但机制不同：它把 nacos 换成进程内的 HTTP 桩，判的是
# SDK 构造期**同步发出**的那个登录请求（server_addrs/username/password 三行的直读口），
# 所以也不需要服务容器；同一个包另外六条 `TestMappingBackend_*` 要真后端，不进本表，
# 由 ci.yml 的 integration 作业点名（docscheck 认那条路径）。
# contrib/database/mongodb 那条（§7.23）的离线桩同样是进程内的——`net.Listen` 出来的
# 「接受 TCP 但永不回 MongoDB 握手」的假线。八行里 uri / server_selection_timeout /
# connect_timeout 三行，加上 database 的「缺席语义」，共四条腿能离线读；database 取值、
# credentials 两态、heartbeat、max_conn_idle 共五条腿要真后端，走 ci.yml 里 mongo:7
# 服务点名，不进本表；timeout_seconds 一行**只有** `contract_test.go` 的长度判据——上游把
# 每个操作的截止期写死成 10s，恰好掩掉契约值，运行期两侧都判不到（理由写在该包
# mapping_backend_test.go 文件头）。
rows() {
  printf '%s\n' \
    $'ci\tpkg\t./appkit\tTestPlan_(NoSideEffects|OrderInvariant|SegmentCoverage|Conflict|DomainRoutes|CustomRoutesWarning)|TestRouteShapeConflict\t' \
    $'ci\tpkg\t./appkit\tTestTeardownOrder_|TestProviderPlan_StdTeardownOrder|TestComponent_StartFailureRollback|TestMountComponent_MountFailureRollback\t' \
    $'ci\tpkg\t./appkit\tTestDrift\t' \
    $'ci\tpkg\t./appkit\tTestPlanCache_|TestVariantProjection_|TestVariantEffects_|TestOption_OnRoleScoping|TestDoctor_\t' \
    $'ci\tpkg\t./appkit\tTestAPI_\t' \
    $'ci\tcli\t./internal/codegen\tTestGen_(Deterministic|CheckClean)|TestGenDomain_(Bind|Proto|NoRoutes|CheckDoesNotRunProtoStage)|TestGenApp_Variants|TestTree_Variants|TestTree_CustomWiring|TestPruneStaleOwned|TestCheckTree_\t' \
    $'ci\tregistry/etcd\t.\tTestRetreatDelay_|TestRetreatPolicy_|TestHeartBeatRetreat\t' \
    $'ci\tregistry/etcd\t./contract\tTestRetreat_|TestProvider_BackoffSection|TestProvider_ContractSections|TestProvider_MaxRetrySection|TestProvider_AuthSection\t' \
    $'ci\tregistry/consul\t./contract\tTestProvider_RegisterBody|TestProvider_CheckFlags|TestProvider_AbsentBools|TestProvider_AbsentIntervals|TestProvider_SchemeSection|TestProvider_DatacenterSection\t' \
    $'ci\ttransport/asynq\t./contract\tTestMapping_\t' \
    $'ci\tcontrib/database/gorm\t./contract\tTestMapping_|TestAllowedDriversTableShape\t' \
    $'ci\tregistry/nacos\t./contract\tTestMappingOffline_\t' \
    $'ci\tcontrib/database/mongodb\t./contract\tTestMappingOffline_\t' \
    $'ci\tbconf\t.\tTestUnmarshalNestedBackoffSection|TestUnmarshalAbsentBackoffStaysNil|TestUnmarshalDurationSpellings|TestCoerceDurationFormat\t' \
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
