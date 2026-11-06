#!/bin/sh
# 按仓里的两把 pin 装 shellcheck：版本读 scripts/shellcheck.version，字节读 scripts/shellcheck.sha256。
#
# 谁在用：只有 `.github/workflows/workflow-lint.yml` 的 `actionlint` 作业。runner 镜像预装一把
# shellcheck，版本随镜像漂移——actionlint 的 run 脚本那条腿就是外部调用 PATH 上的 shellcheck
# （§7.30 记的那一半闸门），所以「本地和 CI 判的是不是同一个规则集」取决于两侧 shellcheck 同版本。
# 本机不进这条脚本：本机用 Homebrew/已装的 shellcheck，由 Taskfile `ci-workflow-lint` 的版本判据
# （读同一个 scripts/shellcheck.version）保证本机就是同一版本。口径与 install-buf.sh 同源：
# 抽成一个文件而不是在作业里内联，是因为「下载 + 摘要校验 + 解包」这几步一旦复制两份，
# 一处改了摘要另一处没改不会报错，只会让 CI 在错误的二进制上跑绿。
#
# 用法：sh scripts/install-shellcheck.sh <安装目录>
# 退出非零的情形都是「别放行」：版本空、摘要表里没这个架构、下载失败、摘要不符、解包后没找到二进制。
set -e

target_dir=${1:?用法: sh scripts/install-shellcheck.sh <安装目录>}

root=$(cd "$(dirname "$0")/.." && pwd)

v=$(tr -d '[:space:]' < "$root/scripts/shellcheck.version")
if [ -z "$v" ]; then
  echo "install-shellcheck: scripts/shellcheck.version 是空的——装哪把都没有出处" >&2
  exit 1
fi

# asset 名跟着 runner 的架构走；摘要按 **架构**索引（版本已由 shellcheck.version 单独 pin，
# 这份文件只钉「该架构 tar.xz 的字节」）。
case $(uname -m) in
  x86_64 | amd64) arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 ;;
  *)
    echo "install-shellcheck: 不认识的架构 $(uname -m)，没有对应的官方 asset" >&2
    exit 1
    ;;
esac

want=$(awk -v a="$arch" '$1 == a {print $2}' "$root/scripts/shellcheck.sha256")
if [ -z "$want" ]; then
  echo "install-shellcheck: scripts/shellcheck.sha256 里没有 $arch 这一行——摘要没出处就别放行" >&2
  exit 1
fi

asset="shellcheck-v${v}.linux.${arch}.tar.xz"
tmp=$(mktemp)
work=$(mktemp -d)
trap 'rm -f "$tmp"; rm -rf "$work"' EXIT

curl -fsSL -o "$tmp" "https://github.com/koalaman/shellcheck/releases/download/v${v}/${asset}"

# --status：不符即非零，且不再打印逐行结果——红的时候要点名摘要，不能靠人翻日志。
if ! echo "$want  $tmp" | sha256sum --check --status; then
  echo "install-shellcheck: $asset 的 SHA-256 与 scripts/shellcheck.sha256 不符" >&2
  echo "  期望 $want" >&2
  echo "  实得 $(sha256sum "$tmp" | awk '{print $1}')" >&2
  exit 1
fi

# tar.xz 里是 shellcheck-v${v}/shellcheck；解到临时目录再取那一枚二进制。
tar -xJf "$tmp" -C "$work"
src=$(find "$work" -type f -name shellcheck | head -1)
if [ -z "$src" ] || [ ! -f "$src" ]; then
  echo "install-shellcheck: $asset 解包后没找到 shellcheck 二进制——不放行" >&2
  exit 1
fi

mkdir -p "$target_dir"
mv "$src" "$target_dir/shellcheck"
chmod +x "$target_dir/shellcheck"
trap - EXIT
rm -f "$tmp"; rm -rf "$work"

echo "install-shellcheck: 装好 shellcheck v${v}（$arch，摘要已按 scripts/shellcheck.sha256 校验）→ $target_dir/shellcheck"
echo "install-shellcheck: shellcheck --version -> $("$target_dir/shellcheck" --version | sed -n 's/^version: //p')"
