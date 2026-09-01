#!/bin/sh
set -eu

mc_bin="${YUNLING_MC_BIN:-mc}"
secret_dir="${YUNLING_MINIO_SECRET_DIR:-/run/secrets}"
policy_source="${YUNLING_MINIO_POLICY_SOURCE:-/opt/yunling/minio-backup-policy.json}"
policy_target="${YUNLING_MINIO_POLICY_TARGET:-/tmp/minio-backup-policy.json}"
access_key_file="${secret_dir}/backup-minio-access-key"
secret_key_file="${secret_dir}/backup-minio-secret-key"

for required_value in \
    "${YUNLING_MINIO_ROOT_USER:-}" \
    "${YUNLING_MINIO_ROOT_PASSWORD:-}" \
    "${YUNLING_S3_BUCKET:-}"; do
    if [ -z "$required_value" ]; then
        echo "MinIO 初始化缺少必需配置" >&2
        exit 1
    fi
done
for required_file in "$access_key_file" "$secret_key_file" "$policy_source"; do
    if [ ! -s "$required_file" ]; then
        echo "MinIO 初始化缺少必需文件" >&2
        exit 1
    fi
done

if ! "$mc_bin" alias set local http://minio:9000 \
    "$YUNLING_MINIO_ROOT_USER" "$YUNLING_MINIO_ROOT_PASSWORD" >/dev/null 2>&1; then
    echo "MinIO 管理连接初始化失败" >&2
    exit 1
fi
if ! "$mc_bin" mb --ignore-existing "local/$YUNLING_S3_BUCKET" >/dev/null 2>&1; then
    echo "MinIO 存储桶初始化失败" >&2
    exit 1
fi
if ! "$mc_bin" anonymous set none "local/$YUNLING_S3_BUCKET" >/dev/null 2>&1; then
    echo "MinIO 存储桶私有访问设置失败" >&2
    exit 1
fi

backup_access_key=""
backup_secret_key=""
IFS= read -r backup_access_key < "$access_key_file" || :
IFS= read -r backup_secret_key < "$secret_key_file" || :
if [ -z "$backup_access_key" ] || [ -z "$backup_secret_key" ]; then
    echo "MinIO 备份账号密钥为空" >&2
    exit 1
fi
while IFS= read -r policy_line || [ -n "$policy_line" ]; do
    while [ "${policy_line#*__BUCKET__}" != "$policy_line" ]; do
        policy_prefix="${policy_line%%__BUCKET__*}"
        policy_suffix="${policy_line#*__BUCKET__}"
        policy_line="${policy_prefix}${YUNLING_S3_BUCKET}${policy_suffix}"
    done
    printf '%s\n' "$policy_line"
done < "$policy_source" > "$policy_target"

if "$mc_bin" admin user info local "$backup_access_key" >/dev/null 2>&1; then
    if ! "$mc_bin" admin user enable local "$backup_access_key" >/dev/null 2>&1; then
        echo "MinIO 备份账号启用失败" >&2
        exit 1
    fi
else
    if ! "$mc_bin" admin user add local "$backup_access_key" "$backup_secret_key" >/dev/null 2>&1; then
        echo "MinIO 备份账号创建失败" >&2
        exit 1
    fi
fi
if ! "$mc_bin" admin policy create local yunling-backup-readonly "$policy_target" >/dev/null 2>&1; then
    echo "MinIO 备份策略创建失败" >&2
    exit 1
fi
if ! "$mc_bin" admin policy attach local yunling-backup-readonly \
    --user "$backup_access_key" >/dev/null 2>&1; then
    echo "MinIO 备份策略绑定失败" >&2
    exit 1
fi

unset backup_access_key backup_secret_key
echo "MinIO 只读备份账号与策略已就绪"
