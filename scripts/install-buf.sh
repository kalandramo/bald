#!/bin/sh
# 按仓里的两把 pin 装 buf：版本读 bconf/buf.version，字节读 bconf/buf.sha256。
#
# 谁在用：CI 的 proto-contract 与 example 两个作业（本机不需要它——本机用 Homebrew、
# 源码构建或已装的 buf 都行，字节判据本身不校验二进制来源，见 risk-review.md §7.9 未闭合③）。
# 抽成一个文件而不是在每个作业里内联，是因为两处装的 buf 必须同源同摘要：内联两份
# 时「一处改了摘要另一处没改」不会报错，只会让某一个作业在错误的二进制上跑绿。
#
# 用法：sh scripts/install-buf.sh <安装目录>
# 退出非零的情形都是「别放行」：版本空、摘要表里没这个 asset、下载失败、摘要不符。
set -e

target_dir=${1:?用法: sh scripts/install-buf.sh <安装目录>}

root=$(cd "$(dirname "$0")/.." && pwd)

v=$(tr -d '[:space:]' < "$root/bconf/buf.version")
if [ -z "$v" ]; then
  echo "install-buf: bconf/buf.version 是空的——装哪把都没有出处" >&2
  exit 1
fi

# asset 名跟着 runner 的架构走；摘要按 **asset 名**索引（版本已由 buf.version 单独 pin，
# 这份文件只钉「该 asset 的字节」）。
case $(uname -m) in
  x86_64 | amd64) asset=Linux-x86_64 ;;
  aarch64 | arm64) asset=Linux-aarch64 ;;
  *)
    echo "install-buf: 不认识的架构 $(uname -m)，没有对应的官方 asset" >&2
    exit 1
    ;;
esac

want=$(awk -v a="$asset" '$1 == a {print $2}' "$root/bconf/buf.sha256")
if [ -z "$want" ]; then
  echo "install-buf: bconf/buf.sha256 里没有 $asset 这一行——摘要没出处就别放行" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL -o "$tmp" "https://github.com/bufbuild/buf/releases/download/v${v}/buf-${asset}"

# --status：不符即非零，且不再打印逐行结果——红的时候要点名摘要，不能靠人翻日志。
if ! echo "$want  $tmp" | sha256sum --check --status; then
  echo "install-buf: $asset v${v} 的 SHA-256 与 bconf/buf.sha256 不符" >&2
  echo "  期望 $want" >&2
  echo "  实得 $(sha256sum "$tmp" | awk '{print $1}')" >&2
  exit 1
fi

mkdir -p "$target_dir"
mv "$tmp" "$target_dir/buf"
chmod +x "$target_dir/buf"
trap - EXIT

echo "install-buf: 装好 buf v${v}（$asset，摘要已按 bconf/buf.sha256 校验）→ $target_dir/buf"
echo "install-buf: buf --version -> $("$target_dir/buf" --version)"
