#!/bin/sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

printf '#!/bin/sh\necho amd64\n' >"$test_dir/amd64"
printf '#!/bin/sh\necho arm64\n' >"$test_dir/arm64"
chmod 0755 "$test_dir/amd64" "$test_dir/arm64"

sh "$root_dir/deploy/agent/package.sh" \
  0.1.0 "$test_dir/amd64" "$test_dir/arm64" "$test_dir/out"

test "$(find "$test_dir/out" -type f | wc -l | tr -d ' ')" = 3
for arch in amd64 arm64; do
  archive="$test_dir/out/yunling-agent-0.1.0-linux-$arch.tar.gz"
  test -f "$archive"
  contents=$(tar -tzf "$archive" | sort | tr '\n' ' ')
  test "$contents" = "50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service "

  sha256=$(sha256sum "$archive" | cut -d ' ' -f 1)
  byte_size=$(wc -c <"$archive" | tr -d ' ')
  grep -Fq "\"os\":\"linux\",\"arch\":\"$arch\",\"file_name\":\"yunling-agent-0.1.0-linux-$arch.tar.gz\",\"byte_size\":$byte_size,\"sha256\":\"$sha256\"" \
    "$test_dir/out/manifest.json"
done
grep -Fq '"version":"0.1.0"' "$test_dir/out/manifest.json"

if sh "$root_dir/deploy/agent/package.sh" \
  'bad/version' "$test_dir/amd64" "$test_dir/arm64" "$test_dir/bad-version"; then
  echo '版本包含非法字符时必须失败' >&2
  exit 1
fi

if sh "$root_dir/deploy/agent/package.sh" \
  0.1.0 "$test_dir/missing" "$test_dir/arm64" "$test_dir/missing-binary"; then
  echo '代理二进制缺失时必须失败' >&2
  exit 1
fi

printf 'not executable\n' >"$test_dir/not-executable"
if sh "$root_dir/deploy/agent/package.sh" \
  0.1.0 "$test_dir/not-executable" "$test_dir/arm64" "$test_dir/not-executable-output"; then
  echo '代理二进制不可执行时必须失败' >&2
  exit 1
fi
