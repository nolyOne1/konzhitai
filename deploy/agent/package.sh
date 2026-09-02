#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
  echo "用法：package.sh 版本 AMD64二进制 ARM64二进制 输出目录" >&2
  exit 1
fi

version=$1
amd64_binary=$2
arm64_binary=$3
output_dir=$4
root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)

case "$version" in
  ""|*[!0-9A-Za-z._-]*)
    echo "代理版本格式无效：$version" >&2
    exit 1
    ;;
esac
case "$version" in
  [0-9A-Za-z]*) ;;
  *)
    echo "代理版本必须以字母或数字开头：$version" >&2
    exit 1
    ;;
esac
if [ "${#version}" -gt 64 ]; then
  echo "代理版本长度不能超过 64 个字符。" >&2
  exit 1
fi

for binary in "$amd64_binary" "$arm64_binary"; do
  if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    echo "代理二进制不存在、不是普通文件或不可执行：$binary" >&2
    exit 1
  fi
done

for asset in install.sh yunling-agent.service yunling-run@.service 50-yunling-agent.rules; do
  if [ ! -f "$root_dir/deploy/agent/$asset" ]; then
    echo "代理安装资产缺失：$asset" >&2
    exit 1
  fi
done

mkdir -p "$output_dir"

package_arch() {
  arch=$1
  binary=$2
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' 0 HUP INT TERM

  install -m 0755 "$binary" "$stage/yunling-agent"
  install -m 0755 "$root_dir/deploy/agent/install.sh" "$stage/install.sh"
  install -m 0644 \
    "$root_dir/deploy/agent/yunling-agent.service" \
    "$root_dir/deploy/agent/yunling-run@.service" \
    "$root_dir/deploy/agent/50-yunling-agent.rules" \
    "$stage/"

  file_name="yunling-agent-${version}-linux-${arch}.tar.gz"
  archive="$output_dir/$file_name"
  tar -czf "$archive" -C "$stage" \
    50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service
  sha256=$(sha256sum "$archive" | cut -d ' ' -f 1)
  byte_size=$(wc -c <"$archive" | tr -d ' ')

  rm -rf "$stage"
  trap - 0 HUP INT TERM
  printf '{"os":"linux","arch":"%s","file_name":"%s","byte_size":%s,"sha256":"%s"}' \
    "$arch" "$file_name" "$byte_size" "$sha256"
}

amd64_artifact=$(package_arch amd64 "$amd64_binary")
arm64_artifact=$(package_arch arm64 "$arm64_binary")
printf '{"version":"%s","artifacts":[%s,%s]}\n' \
  "$version" "$amd64_artifact" "$arm64_artifact" >"$output_dir/manifest.json"
