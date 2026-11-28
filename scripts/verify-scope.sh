#!/bin/sh
# 按目录前缀验证（S2 扁平化 2026-11-28 起）。
#
# 为什么重写：旧 task verify-scope 用「list-modules 按前缀滤 module」再逐 module
# 跑 verify-module。扁平化后 retry/transport/broker 等前缀不再是 module——派生
# 恒为空，「空即响」的守卫会让每个前缀组必红。本脚本把归属改成「最长前缀 module
# 拥有者」×「该 module 内的包集合」，命令面与 verify-module 对齐（build 的 main
# 分支、vet copylocks、-short test、bench 冒烟都在）；`go mod tidy -diff` 是
# 整 module 判据，scope 跑不携带，仍由 CI / task verify-modules 负责。
#
# 用法：sh scripts/verify-scope.sh <dir-prefix>（可带 ./ 前缀）
# 退出码：前缀无归属包 = 响（目录改名/谓词漂移与漏跑同罪）；任一命令红 = 非零。
set -e

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

prefix="${1#./}"
if [ -z "$prefix" ]; then
  echo "verify-scope: 需要 <dir-prefix>，例：sh scripts/verify-scope.sh transport" >&2
  exit 2
fi
if [ ! -d "$prefix" ]; then
  echo "verify-scope: $prefix 不是仓库内目录" >&2
  exit 1
fi

# 归属 module：非根 module 按最长前缀命中，否则落聚合根（扁平形态的大多数前缀）。
mod="."
for m in $(sh scripts/list-modules.sh); do
  c="${m#./}"
  case "$prefix" in
    "$c"|"$c"/*)
      if [ "${#c}" -gt "${#mod}" ]; then mod="$c"; fi
      ;;
  esac
done

if [ "$mod" = "." ]; then
  spec="./$prefix/..."
  dir="$root"
else
  spec="./${prefix#"$mod"/}/..."
  dir="$root/$mod"
fi

if [ -z "$( cd "$dir" && go list "$spec" 2>/dev/null )" ]; then
  echo "verify-scope: $prefix 在 module $mod 下没有任何 Go 包（目录改名了或谓词被改坏）" >&2
  exit 1
fi
echo "verify-scope: $prefix → module $mod, $spec"

cd "$dir"
if go list -json "$spec" | grep -q '"Name": "main"'; then
  go build -o "$(mktemp -d)" "$spec"
else
  go build "$spec"
fi
if [ "$mod" = "." ]; then
  go vet -copylocks=false "$spec"
else
  go vet "$spec"
fi
go test -short -timeout 5m -count=1 "$spec"
go test -run '^$' -bench . -benchtime=1x "$spec"
