#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
installer="${repo_root}/deploy/release/install.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT HUP INT TERM

fake_bin="${test_root}/fake-bin"
mkdir -p "${fake_bin}"
log="${test_root}/commands.log"

make_fake() {
  local name="$1"
  cat >"${fake_bin}/${name}" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
name="$(basename -- "$0")"
printf '%s %s\n' "${name}" "$*" >>"${YUNLING_TEST_LOG}"
if [[ "${YUNLING_FAIL_COMMAND:-}" == "${name}" ]]; then
  exit 71
fi
case "${name}" in
  getent|id) exit 1 ;;
  useradd|usermod|visudo|sshd|docker|systemctl|chown|chmod) exit 0 ;;
  install)
    mode=''
    directory=0
    operands=()
    while (($#)); do
      case "$1" in
        -d) directory=1; shift ;;
        -m) mode="$2"; shift 2 ;;
        -o|-g) shift 2 ;;
        --) shift; break ;;
        -*) shift ;;
        *) operands+=("$1"); shift ;;
      esac
    done
    while (($#)); do operands+=("$1"); shift; done
    if ((directory)); then
      mkdir -p "${operands[@]}"
    else
      mkdir -p "$(dirname -- "${operands[-1]}")"
      cp -- "${operands[0]}" "${operands[-1]}"
    fi
    [[ -z "${mode}" ]] || chmod "${mode}" "${operands[-1]}"
    ;;
esac
SCRIPT
  chmod 0755 "${fake_bin}/${name}"
}

for command_name in getent id useradd usermod install visudo sshd ssh-keygen docker systemctl chown chmod; do
  make_fake "${command_name}"
done

new_binary="${test_root}/yunling-release-linux-amd64"
printf 'new-release-binary\n' >"${new_binary}"
chmod 0755 "${new_binary}"

run_installer() {
  YUNLING_INSTALL_TESTING=1 \
  YUNLING_INSTALL_TEST_EUID=0 \
  YUNLING_INSTALL_TEST_OWNER=root \
  YUNLING_INSTALL_TEST_MODE=600 \
  YUNLING_INSTALL_ROOT="$1" \
  YUNLING_RELEASE_BINARY="${new_binary}" \
  YUNLING_TEST_LOG="${log}" \
  YUNLING_COMMAND_PATH="${fake_bin}" \
  YUNLING_FAIL_COMMAND="${2:-}" \
    bash "${installer}" --public-key-file "$3"
}

bad_root="${test_root}/bad-root"
mkdir -p "${bad_root}" "${test_root}/keys"
bad_key="${test_root}/keys/bad.pub"
printf 'ssh-rsa AAAAB3NzaInvalid test\n' >"${bad_key}"
chmod 0600 "${bad_key}"
if run_installer "${bad_root}" '' "${bad_key}"; then
  echo '非 Ed25519 公钥必须失败' >&2
  exit 1
fi
test ! -e "${bad_root}/etc/sudoers.d/yunling-deploy"
test ! -e "${bad_root}/var/lib/yunling-deploy/.ssh/authorized_keys"

good_key="${test_root}/keys/good.pub"
printf 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKWcWj2h4QO9eqM8Xz4L9QbQbOSUuqkwWJpooP8T7Xz9 deploy\n' >"${good_key}"
chmod 0600 "${good_key}"

if YUNLING_INSTALL_TESTING=0 YUNLING_INSTALL_ROOT="${test_root}/forbidden-root" \
  YUNLING_INSTALL_TEST_EUID=0 YUNLING_INSTALL_TEST_OWNER=root YUNLING_INSTALL_TEST_MODE=600 \
  bash "${installer}" --public-key-file "${good_key}" >/dev/null 2>&1; then
  echo '生产模式不得接受测试环境绕过项' >&2
  exit 1
fi

for failed_command in useradd install visudo sshd docker systemctl; do
  failure_root="${test_root}/failure-${failed_command}"
  mkdir -p "${failure_root}/usr/local/sbin"
  printf 'working-release-binary\n' >"${failure_root}/usr/local/sbin/yunling-release"
  if run_installer "${failure_root}" "${failed_command}" "${good_key}" >/dev/null 2>&1; then
    echo "${failed_command} 失败时安装器必须失败" >&2
    exit 1
  fi
  test "$(cat "${failure_root}/usr/local/sbin/yunling-release")" = 'working-release-binary'
  test ! -e "${failure_root}/var/lib/yunling-deploy/.ssh/authorized_keys"
done

success_root="${test_root}/success-root"
run_installer "${success_root}" '' "${good_key}"
test "$(cat "${success_root}/usr/local/sbin/yunling-release")" = 'new-release-binary'
test "$(cat "${success_root}/etc/sudoers.d/yunling-deploy")" = \
  'yunling-deploy ALL=(root) NOPASSWD: /usr/local/sbin/yunling-release execute'
test "$(cat "${success_root}/var/lib/yunling-deploy/.ssh/authorized_keys")" = \
  'restrict,command="/usr/bin/sudo -n /usr/local/sbin/yunling-release execute" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKWcWj2h4QO9eqM8Xz4L9QbQbOSUuqkwWJpooP8T7Xz9 deploy'
grep -Fq 'useradd --system --home-dir /var/lib/yunling-deploy --shell /bin/sh' "${log}"
grep -Fq 'visudo -cf' "${log}"
grep -Fq 'sshd -T' "${log}"
grep -Fq 'docker version' "${log}"
grep -Fq 'systemctl --version' "${log}"
