#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || -z "$1" || "$1" == -* ]]; then
  echo '用法：bash deploy/ops_mc_smoke_test.sh 已构建的Ops镜像' >&2
  exit 2
fi

# Use the final image's own user. No Compose services, host mounts or network.
docker run --rm --interactive --pull never --network none --read-only \
  --security-opt no-new-privileges:true --cap-drop ALL \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16m,mode=1777 \
  --entrypoint /bin/sh "$1" -eu -s <<'CONTAINER'
test "$(id -u)" = 10001
test "$(id -g)" = 10001
version=$(/usr/bin/mc --version)
printf '%s\n' "$version"
printf '%s\n' "$version" | grep -Fx 'mc version RELEASE.2025-08-13T08-35-41Z (commit-id=7394ce0dd2a80935aded936b09fa12cbb3cb8096)'
for notice in LICENSE CREDITS NOTICE; do
  test -s "/usr/share/licenses/minio-mc/$notice"
done
sha256sum /usr/bin/mc

checkdir=$(mktemp -d /tmp/mc-check.XXXXXX)
mkdir "$checkdir/source" "$checkdir/destination"
printf 'new-content\n' > "$checkdir/source/current.txt"
printf 'new-file\n' > "$checkdir/source/added.txt"
printf 'obsolete-longer-content\n' > "$checkdir/destination/current.txt"
printf 'stale\n' > "$checkdir/destination/stale.txt"
/usr/bin/mc --config-dir "$checkdir/config" mirror --overwrite --remove "$checkdir/source" "$checkdir/destination"
cmp "$checkdir/source/current.txt" "$checkdir/destination/current.txt"
cmp "$checkdir/source/added.txt" "$checkdir/destination/added.txt"
test ! -e "$checkdir/destination/stale.txt"
printf 'MC_SMOKE_OK user=10001 version=RELEASE.2025-08-13T08-35-41Z overwrite=ok remove=ok\n'
CONTAINER
