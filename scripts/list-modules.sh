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

# 派生失败必须响：`find | xargs | sort` 的退出码取的是管道最后一个命令（sort），
# xargs 半途死掉（本机实测过 `xargs: cannot fork: Resource temporarily unavailable`）
# 时清单会**静默变短**，而下游闸门只判「非空」——覆盖率缩水仍然全绿。
# 所以把 xargs 放在管道末尾（其退出码即整个替换的退出码），sort 单独一步。
raw=$(find . -name go.mod -not -path "./.git/*" -not -path "./_example*" | xargs -n1 dirname) || {
  echo "list-modules: find/xargs 失败——清单可能被截断，下游会按缩水的覆盖跑绿" >&2
  exit 1
}
list=$(printf '%s\n' "$raw" | sort)

if [ -z "$list" ]; then
  echo "list-modules: 在 $root 下未派生出任何 module，清单不可用" >&2
  exit 1
fi

printf '%s\n' "$list"
