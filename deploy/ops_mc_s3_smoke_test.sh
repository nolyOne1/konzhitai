#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 || -z "$1" || -z "$2" || "$1" == -* || "$2" == -* ]]; then
  echo '用法：bash deploy/ops_mc_s3_smoke_test.sh 已构建的Ops镜像 已构建的MinIO测试镜像' >&2
  exit 2
fi

ops_image=$1
minio_image=$2
prefix="yunling-mc-s3-smoke-$(openssl rand -hex 8)"
network_name="${prefix}-net"
server_name="${prefix}-minio"
client_name="${prefix}-ops"
network_id=''
server_id=''
client_id=''

remove_container() {
  local container_id=$1 expected_name=$2 actual_name
  [[ -n "$container_id" ]] || return 0
  [[ "$expected_name" == "$prefix-"* ]] || return 1
  actual_name=$(docker container inspect --format '{{.Name}}' "$container_id") || return 1
  [[ "$actual_name" == "/$expected_name" ]] || return 1
  docker container rm --force "$container_id" >/dev/null
}

cleanup() {
  local status=$? actual_name
  trap - EXIT
  remove_container "$client_id" "$client_name" || status=1
  remove_container "$server_id" "$server_name" || status=1
  if [[ -n "$network_id" ]]; then
    actual_name=$(docker network inspect --format '{{.Name}}' "$network_id") || status=1
    if [[ "$network_name" == "$prefix-"* && "$actual_name" == "$network_name" ]]; then
      docker network rm "$network_id" >/dev/null || status=1
    else
      status=1
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Test-only random credentials stay in process/container environment, never logs.
MINIO_ROOT_USER="mc-$(openssl rand -hex 8)"
MINIO_ROOT_PASSWORD=$(openssl rand -hex 16)
MC_HOST_local="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@minio:9000"
export MINIO_ROOT_USER MINIO_ROOT_PASSWORD MC_HOST_local
network_id=$(docker network create --internal "$network_name")
server_id=$(docker create --pull never --name "$server_name" \
  --network "$network_name" --network-alias minio \
  --user 10001:10001 --read-only --security-opt no-new-privileges:true --cap-drop ALL \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=2g,uid=10001,gid=10001,mode=0700 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=32m,mode=1777 \
  --env MINIO_ROOT_USER --env MINIO_ROOT_PASSWORD --env MINIO_BROWSER=off \
  "$minio_image" server /data --address :9000 --certs-dir /tmp/certs)
docker start "$server_id" >/dev/null
ready=false
for ((attempt=0; attempt<30; attempt++)); do
  if docker exec "$server_id" curl --max-time 1 -fsS http://127.0.0.1:9000/minio/health/ready >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo '一次性 MinIO 测试容器未就绪' >&2
  exit 1
fi

# Default to the actual ops image user; no host mounts, production environment or ports.
client_id=$(docker create --interactive --pull never --name "$client_name" \
  --network "$network_name" --read-only --security-opt no-new-privileges:true --cap-drop ALL \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=32m,mode=1777 \
  --env MC_HOST_local --env MC_UPDATE=off --entrypoint /bin/sh "$ops_image" -eu -s)
docker start --attach --interactive "$client_id" <<'CONTAINER'
test "$(id -u)" = 10001
test "$(id -g)" = 10001
checkdir=$(mktemp -d /tmp/mc-s3-check.XXXXXX)
mkdir "$checkdir/source" "$checkdir/destination"
mc() { /usr/bin/mc --config-dir "$checkdir/config" "$@"; }
printf 's3-new-content\n' > "$checkdir/source/current.txt"
printf 's3-new-file\n' > "$checkdir/source/added.txt"
mc mb local/backup-fixture
mc cp "$checkdir/source/current.txt" local/backup-fixture/current.txt
mc cp "$checkdir/source/added.txt" local/backup-fixture/added.txt
printf 'obsolete-longer-content\n' > "$checkdir/destination/current.txt"
printf 'stale\n' > "$checkdir/destination/stale.txt"
mc mirror --overwrite --remove local/backup-fixture "$checkdir/destination"
cmp "$checkdir/source/current.txt" "$checkdir/destination/current.txt"
cmp "$checkdir/source/added.txt" "$checkdir/destination/added.txt"
test ! -e "$checkdir/destination/stale.txt"
printf 'S3_SMOKE_OK user=10001 source=fixture-bucket overwrite=ok remove=ok\n'
CONTAINER
test "$(docker container inspect --format '{{.State.Running}}' "$client_id")" = false
test "$(docker container inspect --format '{{.State.ExitCode}}' "$client_id")" = 0
