#!/bin/sh
# 多 major 并存的只读报告：对每个 module 读 `go mod edit -json` 的 require，
# 把同一基础路径（去掉尾部 /vN）下出现两个及以上不同 major 的组合列出来。
#
# 只读——不改任何 go.mod、不做 exclude/replace、退出码恒为 0。它是给接手者的
# 观测面，不是闸门：多数并存来自上游 SDK 的传递依赖，本身不是正确性问题。
#
# 用法：sh scripts/multi-major.sh             （全部 module）
#       sh scripts/multi-major.sh oss/minio   （只看指定 module）
set -e

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ $# -gt 0 ]; then
  mods=$(for m in "$@"; do printf '%s\n' "${m#./}"; done)
else
  mods=$(sh scripts/list-modules.sh | sed 's|^\./||')
fi

for m in $mods; do
  [ -f "$m/go.mod" ] || continue
  go mod edit -json "$m/go.mod" | awk -v mod="$m" '
    # go mod edit -json 把每个 require 的 Path/Version 各占一行，按行序配对。
    /"Path":/    { path = $2 }
    /"Version":/ {
      ver = $2
      gsub(/[" ,]/, "", path)
      gsub(/[" ,]/, "", ver)
      base = path
      major = ""
      if (match(path, /\/v[0-9]+$/)) {
        base = substr(path, 1, RSTART - 1)
        major = substr(path, RSTART + 1)
      }
      if (!((base SUBSEP major) in first)) first[base SUBSEP major] = ver
      majors[base]++
    }
    END {
      for (b in majors) {
        if (majors[b] > 1) {
          out = ""
          split("", done)
          for (k in first) {
            split(k, p, SUBSEP)
            if (p[1] == b) out = out " " (p[2] == "" ? "(无 major)" : p[2]) "=" first[k]
          }
          printf "%s\t%s\t%s\n", mod, b, out
        }
      }
    }
  '
done | sort | awk -F'\t' '
  BEGIN { printf "%-30s %-48s %s\n", "module", "base path", "coexisting majors" }
  { printf "%-30s %-48s %s\n", $1, $2, $3 }
  END { printf "\n合计 %d 条（module × 基础路径）\n", NR }
'
