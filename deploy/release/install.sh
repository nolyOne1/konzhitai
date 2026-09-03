#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install_root=${YUNLING_INSTALL_ROOT:-/}
if [ "${YUNLING_INSTALL_TESTING:-0}" = 1 ]; then
  [ "$install_root" != / ] || { echo '测试模式不得写入真实系统根目录。' >&2; exit 1; }
  source_binary=${YUNLING_RELEASE_BINARY:-"${script_dir}/yunling-release-linux-amd64"}
else
  PATH=/usr/sbin:/usr/bin:/sbin:/bin
  export PATH
  source_binary="${script_dir}/yunling-release-linux-amd64"
fi
public_key_file=''
stage_dir=''
binary_installed=0
sudoers_installed=0
authorized_keys_installed=0

usage() {
  echo '用法：install.sh --public-key-file /root/yunling-deploy.pub' >&2
}

fail() {
  echo "$1" >&2
  exit 1
}

target_path() {
  case "$install_root" in
    /) printf '%s\n' "$1" ;;
    *) printf '%s%s\n' "${install_root%/}" "$1" ;;
  esac
}

command_path() {
  name=$1
  if [ "${YUNLING_INSTALL_TESTING:-0}" = 1 ] && [ -n "${YUNLING_COMMAND_PATH:-}" ]; then
    printf '%s/%s\n' "${YUNLING_COMMAND_PATH%/}" "$name"
  else
    command -v "$name" 2>/dev/null || return 1
  fi
}

cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ "$status" -ne 0 ] && [ -n "$stage_dir" ]; then
    binary_path=$(target_path /usr/local/sbin/yunling-release)
    sudoers_path=$(target_path /etc/sudoers.d/yunling-deploy)
    authorized_keys_path=$(target_path /var/lib/yunling-deploy/.ssh/authorized_keys)
    if [ "$authorized_keys_installed" -eq 1 ]; then
      if [ -f "$stage_dir/authorized_keys.old" ]; then mv -f "$stage_dir/authorized_keys.old" "$authorized_keys_path"; else rm -f "$authorized_keys_path"; fi
    fi
    if [ "$sudoers_installed" -eq 1 ]; then
      if [ -f "$stage_dir/sudoers.old" ]; then mv -f "$stage_dir/sudoers.old" "$sudoers_path"; else rm -f "$sudoers_path"; fi
    fi
    if [ "$binary_installed" -eq 1 ]; then
      if [ -f "$stage_dir/binary.old" ]; then mv -f "$stage_dir/binary.old" "$binary_path"; else rm -f "$binary_path"; fi
    fi
  fi
  [ -z "$stage_dir" ] || rm -rf "$stage_dir"
  exit "$status"
}

if [ "$#" -ne 2 ] || [ "$1" != '--public-key-file' ]; then
  usage
  exit 2
fi
public_key_file=$2

if [ "${YUNLING_INSTALL_TESTING:-0}" != 1 ] && [ "$install_root" != / ]; then
  fail '生产安装不允许重定向系统根目录。'
fi
if [ "${YUNLING_INSTALL_TESTING:-0}" = 1 ]; then
  effective_uid=${YUNLING_INSTALL_TEST_EUID:-$(id -u)}
else
  effective_uid=$(id -u)
fi
[ "$effective_uid" = 0 ] || fail '请使用 root 权限安装生产发布入口。'

[ -f "$public_key_file" ] && [ ! -L "$public_key_file" ] || fail '公钥必须是普通文件。'
if [ "${YUNLING_INSTALL_TESTING:-0}" = 1 ]; then
  key_mode=${YUNLING_INSTALL_TEST_MODE:-$(stat -c '%a' "$public_key_file" 2>/dev/null || true)}
  key_owner=${YUNLING_INSTALL_TEST_OWNER:-$(stat -c '%U' "$public_key_file" 2>/dev/null || true)}
else
  key_mode=$(stat -c '%a' "$public_key_file" 2>/dev/null || true)
  key_owner=$(stat -c '%U' "$public_key_file" 2>/dev/null || true)
fi
[ "$key_mode" = 600 ] || fail '公钥文件权限必须是 0600。'
[ "$key_owner" = root ] || fail '公钥文件必须归 root 所有。'
public_key=$(awk 'NF { count++; line=$0 } END { if (count != 1) exit 1; print line }' "$public_key_file") ||
  fail '公钥文件必须只包含一行非空内容。'
printf '%s\n' "$public_key" | awk '
  $1 == "ssh-ed25519" && $2 ~ /^[A-Za-z0-9+\/]+={0,2}$/ && (NF == 2 || NF == 3) { ok=1 }
  END { exit(ok ? 0 : 1) }
' || fail '只接受普通 ssh-ed25519 公钥。'

if [ ! -f "$source_binary" ] || [ -L "$source_binary" ]; then
  fail '安装包缺少可执行的 yunling-release-linux-amd64。'
fi
if [ "${YUNLING_INSTALL_TESTING:-0}" != 1 ] && [ ! -x "$source_binary" ]; then
  fail '安装包缺少可执行的 yunling-release-linux-amd64。'
fi

for dependency in getent id useradd usermod install visudo sshd ssh-keygen docker systemctl chown chmod; do
  command_path "$dependency" >/dev/null || fail "缺少安装依赖命令：${dependency}"
done

install_bin=$(command_path install)
visudo_bin=$(command_path visudo)
sshd_bin=$(command_path sshd)
ssh_keygen_bin=$(command_path ssh-keygen)
docker_bin=$(command_path docker)
systemctl_bin=$(command_path systemctl)
useradd_bin=$(command_path useradd)
usermod_bin=$(command_path usermod)
getent_bin=$(command_path getent)
chown_bin=$(command_path chown)
chmod_bin=$(command_path chmod)

"$docker_bin" version >/dev/null 2>&1 || fail 'Docker 不可用，未安装任何生产发布文件。'
"$systemctl_bin" --version >/dev/null 2>&1 || fail 'systemd 不可用，未安装任何生产发布文件。'
"$sshd_bin" -T >/dev/null 2>&1 || fail 'sshd 配置校验失败，未安装任何生产发布文件。'
"$ssh_keygen_bin" -l -f "$public_key_file" >/dev/null 2>&1 || fail 'Ed25519 公钥内容无效，未安装任何生产发布文件。'

sudoers_dir=$(target_path /etc/sudoers.d)
ssh_dir=$(target_path /var/lib/yunling-deploy/.ssh)
binary_dir=$(target_path /usr/local/sbin)
if ! "$getent_bin" passwd yunling-deploy >/dev/null 2>&1; then
  "$useradd_bin" --system --home-dir /var/lib/yunling-deploy --shell /bin/sh --create-home --password '!' yunling-deploy
else
  "$usermod_bin" --lock --home /var/lib/yunling-deploy --shell /bin/sh yunling-deploy
fi
"$install_bin" -d -o root -g root -m 0755 "$sudoers_dir" "$binary_dir"
"$install_bin" -d -o yunling-deploy -g yunling-deploy -m 0700 "$ssh_dir"
stage_parent=$(target_path /var/lib/yunling-deploy)
stage_dir=$(mktemp -d "${stage_parent}/.release-install.XXXXXX")
trap cleanup EXIT HUP INT TERM

"$install_bin" -o root -g root -m 0755 "$source_binary" "$stage_dir/yunling-release"
printf '%s\n' 'yunling-deploy ALL=(root) NOPASSWD: /usr/local/sbin/yunling-release execute' >"$stage_dir/sudoers"
"$chown_bin" root:root "$stage_dir/sudoers"
"$chmod_bin" 0440 "$stage_dir/sudoers"
"$visudo_bin" -cf "$stage_dir/sudoers" >/dev/null
printf '%s %s\n' 'restrict,command="/usr/bin/sudo -n /usr/local/sbin/yunling-release execute"' "$public_key" >"$stage_dir/authorized_keys"
"$chown_bin" yunling-deploy:yunling-deploy "$stage_dir/authorized_keys"
"$chmod_bin" 0600 "$stage_dir/authorized_keys"

binary_path=$(target_path /usr/local/sbin/yunling-release)
sudoers_path=$(target_path /etc/sudoers.d/yunling-deploy)
authorized_keys_path=$(target_path /var/lib/yunling-deploy/.ssh/authorized_keys)
[ ! -e "$binary_path" ] || cp -p "$binary_path" "$stage_dir/binary.old"
[ ! -e "$sudoers_path" ] || cp -p "$sudoers_path" "$stage_dir/sudoers.old"
[ ! -e "$authorized_keys_path" ] || cp -p "$authorized_keys_path" "$stage_dir/authorized_keys.old"

mv -f "$stage_dir/yunling-release" "$binary_path"
binary_installed=1
mv -f "$stage_dir/sudoers" "$sudoers_path"
sudoers_installed=1
mv -f "$stage_dir/authorized_keys" "$authorized_keys_path"
authorized_keys_installed=1

if [ "${YUNLING_INSTALL_TESTING:-0}" != 1 ]; then
  for protected_path in /usr /usr/local /usr/local/sbin /usr/local/sbin/yunling-release /etc/sudoers.d /etc/sudoers.d/yunling-deploy; do
    owner=$(stat -c '%U' "$protected_path")
    permissions=$(stat -c '%a' "$protected_path")
    [ "$owner" = root ] || fail "受保护路径不是 root 所有：${protected_path}"
    case "$permissions" in
      *[2367][0-7]|*[0-7][2367]) fail "受保护路径允许组或其他用户写入：${protected_path}" ;;
    esac
  done
fi

trap - EXIT HUP INT TERM
rm -rf "$stage_dir"
stage_dir=''
echo '受限生产发布账号安装完成；请另行执行 yunling-release bootstrap。'
