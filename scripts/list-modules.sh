#!/bin/sh
# 仓库 Go module 清单的唯一派生。CI（.github/workflows/ci.yml）与本地
# （Taskfile.yml 的 lint/vuln/race/verify）都消费这一条，不再各自内联 find，
# 因此「本地绿」与「CI 覆盖范围」不可能再漂移。
#
# 输出：每行一个 module 目录，相对仓库根、带 ./ 前缀、按字典序；根模块为 "."。
# 谓词：
#   -not -path "./.git/*"     —— .git 里可能有 worktree/stash 残留的 go.mod
#   -not -path "./_example*"  —— 验证用独立 module，由 Taskfile 的 example-* 任务单独覆盖
#
# 清单为空 = 闸门静默失效（谓词被改坏或在错误目录下执行），必须响而不是返回空。
set -e

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

list=$(find . -name go.mod -not -path "./.git/*" -not -path "./_example*" | xargs -n1 dirname | sort)

if [ -z "$list" ]; then
  echo "list-modules: 在 $root 下未派生出任何 module，清单不可用" >&2
  exit 1
fi

printf '%s\n' "$list"
