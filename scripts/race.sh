#!/bin/sh
# race 闸门的唯一目标派生（S2 扁平化 2026-11-28 起）。
#
# 为什么存在：框架曾按 module 一列可发布单元，.github/race-skip.txt 按「module
# 整行」过滤就能把上游库自带竞争的腿摘干净；扁平化后 63 个后端 module 并入根
# 模块，skip 对象（log/aliyun）变成了根模块里的**包目录**——整行过滤既滤不掉
# 它，不过滤又会因 aliyun SDK 内部竞争拖红整个根模块。所以目标派生下沉到
# **包前缀**粒度：逐 module 用 go list 枚举包，命中 race-skip.txt 任一前缀
# （等于该目录或在其之下）的包剔除，其余按 module 一批跑。
#
# 用法：
#   sh scripts/race.sh            # 全部 module（task race / CI 语义的合集）
#   sh scripts/race.sh ./bconf    # 单 module（CI race 作业的下载重试环用）
#
# 退出码：任一入选 go test 判红 → 非零；全仓零入选 = 闸门静默失效 → 非零。
# race-skip 谓词被改坏（全部命中）与清单为空在这里同罪同判。
set -e

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

prefixes=$(grep -v '^[[:space:]]*#' .github/race-skip.txt | grep -v '^[[:space:]]*$' || true)

total_run=0
rc=0

run_module() {
  m="$1"
  if [ ! -f "$m/go.mod" ]; then
    echo "race: $m 没有 go.mod，不是 module（清单谓词漂移？）" >&2
    return 1
  fi
  pkgs=$( cd "$m" && go list ./... )
  kept=""
  n=0
  for p in $pkgs; do
    # bald 系 module 路径全部以 github.com/kalandramo/bald/ 打头，剥前缀即得
    # 仓库根相对形（与 race-skip.txt 条目同一语法：./<dir>）。前缀必须含末尾
    # 斜杠：少剥这道斜杠会得到 `.//dir` 形，后续 `${rel#"$m"/}` 剥不动，
    # module 相对路径被双重拼接（实测 cobramcp 全组 setup failed）。
    rel="./${p#"github.com/kalandramo/bald/"}"
    drop=0
    for e in $prefixes; do
      case "$rel" in
        "$e"|"$e"/*) drop=1; break ;;
      esac
    done
    [ "$drop" -eq 1 ] && continue
    if [ "$rel" = "$m" ]; then
      # module 自身的根包（如 github.com/kalandramo/bald/cobramcp）：
      # 在 module 目录内就是 "."，不能再拼相对路径。
      target="."
    elif [ "$m" = "." ]; then
      target="$rel"
    else
      target="./${rel#"$m"/}"
    fi
    kept="$kept $target"
    n=$((n+1))
  done
  if [ "$n" -eq 0 ]; then
    echo "race: $m 的包经 skip 后无入选（本 module 不参与本次判定）"
    return 0
  fi
  total_run=$((total_run+n))
  echo "=== race: $m（$n 个包）==="
  ( cd "$m" && go test -short -race -timeout 10m $kept )
}

if [ $# -ge 1 ]; then
  # 单 module 模式：调用方（CI）自带失败记账，这里如实透出退出码。
  run_module "$1"
  exit $?
fi

for m in $(sh scripts/list-modules.sh); do
  run_module "$m" || rc=1
done

if [ "$rc" -eq 0 ] && [ "$total_run" -eq 0 ]; then
  echo "race: 全仓没有任何包入选，闸门静默失效（清单或 skip 谓词被改坏）" >&2
  exit 1
fi
exit "$rc"
