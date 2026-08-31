#!/bin/sh
set -eu
umask 077

if [ "$(id -u)" -ne 0 ]; then
    echo "请使用 root 执行密钥初始化脚本" >&2
    exit 1
fi

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
env_file="${script_dir}/.env"
secret_dir="${script_dir}/secrets"
config_dir="${script_dir}/config"
recovery_file=/root/yunling-recovery-key.txt

if [ ! -f "$env_file" ]; then
    echo "缺少 ${env_file}，请先从 .env.example 创建并填写" >&2
    exit 1
fi
if [ "$(stat -c '%U:%G' "$env_file")" != "root:root" ]; then
    echo "${env_file} 必须属于 root:root" >&2
    exit 1
fi
env_mode="$(stat -c '%a' "$env_file")"
case "$env_mode" in
    600|400) ;;
    *) echo "${env_file} 权限必须是 600 或 400" >&2; exit 1 ;;
esac

# deploy/.env 由 root 管理，仅读取 COS 非密钥配置。
# shellcheck disable=SC1090
. "$env_file"

case "${YUNLING_COS_ENDPOINT:-}" in
    https://*) ;;
    *) echo "YUNLING_COS_ENDPOINT 必须使用 HTTPS" >&2; exit 1 ;;
esac
case "${YUNLING_COS_REGION:-}" in
    *[!a-z0-9-]*|'') echo "YUNLING_COS_REGION 格式不正确" >&2; exit 1 ;;
esac
case "${YUNLING_COS_BUCKET:-}" in
    *[!a-z0-9.-]*|'') echo "YUNLING_COS_BUCKET 格式不正确" >&2; exit 1 ;;
esac
case "${YUNLING_COS_PREFIX:-yunling}" in
    *[!A-Za-z0-9._/-]*|'') echo "YUNLING_COS_PREFIX 格式不正确" >&2; exit 1 ;;
esac

install -d -m 0700 -o root -g root "$secret_dir" "$config_dir"

for required_secret in yunling-master-key cos-secret-id cos-secret-key; do
    required_path="${secret_dir}/${required_secret}"
    if [ ! -s "$required_path" ]; then
        echo "缺少 ${required_path}；主密钥和 COS CAM 密钥必须由管理员预先安全写入" >&2
        exit 1
    fi
done

generated_secrets="restic-password backup-postgres-password verify-postgres-password backup-minio-access-key backup-minio-secret-key"
for generated_secret in $generated_secrets; do
    generated_path="${secret_dir}/${generated_secret}"
    if [ -e "$generated_path" ]; then
        echo "拒绝覆盖已有密钥：${generated_path}" >&2
        exit 1
    fi
done
if [ -e "$recovery_file" ]; then
    echo "拒绝覆盖已有恢复材料：${recovery_file}" >&2
    exit 1
fi

generation_dir="$(mktemp -d "${secret_dir}/.generation.XXXXXX")"
cleanup_generation() {
    for generated_secret in $generated_secrets; do
        rm -f "${generation_dir}/${generated_secret}"
    done
    rmdir "$generation_dir" 2>/dev/null || true
}
trap cleanup_generation EXIT HUP INT TERM

openssl rand -base64 48 | tr -d '\r\n' > "${generation_dir}/restic-password"
openssl rand -hex 48 > "${generation_dir}/backup-postgres-password"
openssl rand -hex 48 > "${generation_dir}/verify-postgres-password"
openssl rand -hex 10 > "${generation_dir}/backup-minio-access-key"
openssl rand -base64 30 | tr -d '\r\n' > "${generation_dir}/backup-minio-secret-key"

for generated_secret in $generated_secrets; do
    if [ ! -s "${generation_dir}/${generated_secret}" ]; then
        echo "生成密钥失败：${generated_secret}" >&2
        exit 1
    fi
    install -m 0600 -o root -g root \
        "${generation_dir}/${generated_secret}" "${secret_dir}/${generated_secret}"
done
cleanup_generation
trap - EXIT HUP INT TERM

for secret_path in "$secret_dir"/*; do
    chown root:root "$secret_path"
    chmod 600 "$secret_path"
done

printf '%s\n' '/var/lib/yunling-ops/local-repo' > "${config_dir}/local-repository"
printf 's3:%s/%s/%s\n' \
    "${YUNLING_COS_ENDPOINT%/}" "$YUNLING_COS_BUCKET" "${YUNLING_COS_PREFIX:-yunling}" \
    > "${config_dir}/cos-repository"
chown root:root "${config_dir}/local-repository" "${config_dir}/cos-repository"
chmod 444 "${config_dir}/local-repository" "${config_dir}/cos-repository"

{
    printf '%s\n' '云令灾难恢复材料（只应保存于离线密码库）'
    printf '主密钥文件 Base64：'
    base64 < "${secret_dir}/yunling-master-key" | tr -d '\r\n'
    printf '\nRestic 密码：'
    tr -d '\r\n' < "${secret_dir}/restic-password"
    printf '\n'
} > "$recovery_file"
chown root:root "$recovery_file"
chmod 600 "$recovery_file"

echo "运维密钥与只读配置已生成。"
echo "恢复材料已写入 ${recovery_file}（未显示内容）。"
echo "请先保存到离线密码库，再安全删除服务器上的该文件并确认不存在。"
