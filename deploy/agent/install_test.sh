#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
installer="${root_dir}/deploy/agent/install.sh"
export YUNLING_INSTALL_TESTING=1
source "${installer}"

validate_control_url 'https://aiwise.top'
validate_control_url 'https://control.example:8443'
for invalid_url in \
  'http://aiwise.top' \
  'https://aiwise.top/path' \
  'https://aiwise.top?query=1' \
  'https://user@aiwise.top' \
  'https://aiwise.top:65536'; do
  if validate_control_url "${invalid_url}"; then
    echo "必须拒绝控制平面地址：${invalid_url}" >&2
    exit 1
  fi
done

control_url=''
parse_args --control-url 'https://aiwise.top'
test "${control_url}" = 'https://aiwise.top'
if parse_args 'https://aiwise.top' 2>/dev/null; then
  echo '必须拒绝位置参数' >&2
  exit 1
fi
if parse_args --control-url 'https://aiwise.top' extra 2>/dev/null; then
  echo '必须拒绝额外参数' >&2
  exit 1
fi

test "$(strip_bootstrap_lines $'A=1\nYUNLING_CONTROL_URL=x\nYUNLING_ENROLLMENT_TOKEN=y\nB=2\n')" = $'A=1\nB=2'

if grep -Eq '(^|[^A-Za-z0-9_-])(apt|apt-get|yum|dnf|apk)([^A-Za-z0-9_-]|$)' "${installer}"; then
  echo '安装器不得调用系统包管理器' >&2
  exit 1
fi
for required_text in \
  '/dev/tty' \
  'read -r -s' \
  'trap cleanup_install EXIT' \
  '该服务器已存在云令代理身份，请先在控制台确认后再处理。' \
  'getent' 'groupadd' 'useradd' 'usermod' 'install' 'systemctl' 'mktemp'; do
  grep -Fq "${required_text}" "${installer}"
done
if grep -Fq '${YUNLING_ENROLLMENT_TOKEN' "${installer}"; then
  echo '安装器不得从环境变量读取注册令牌' >&2
  exit 1
fi

test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT HUP INT TERM
cleanup_log="${test_dir}/systemctl.log"
test_token='test-token-must-not-leak'

simulate_failed_install() {
  environment_path="${test_dir}/agent.env"
  credentials_path="${test_dir}/credentials.json"
  registration_complete=0
  service_started=1
  enrollment_token="${test_token}"
  control_url='https://aiwise.top'
  printf 'A=1\nYUNLING_CONTROL_URL=%s\nYUNLING_ENROLLMENT_TOKEN=%s\nB=2\n' \
    "${control_url}" "${enrollment_token}" >"${environment_path}"
  systemctl() { printf '%s\n' "$*" >>"${cleanup_log}"; }
  chown() { return 0; }
  chmod() { return 0; }
  trap cleanup_install EXIT
  return 23
}

set +e
cleanup_output="$( (simulate_failed_install) 2>&1 )"
cleanup_status=$?
set -e
test "${cleanup_status}" -eq 23
if [[ "${cleanup_output}" == *"${test_token}"* ]]; then
  echo '失败清理输出不得包含令牌' >&2
  exit 1
fi
test "$(<"${test_dir}/agent.env")" = $'A=1\nB=2'
grep -Fq 'stop yunling-agent.service' "${cleanup_log}"

simulate_environment_write_failure() {
  environment_path="${test_dir}/write-failure/agent.env"
  mkdir -p "$(dirname -- "${environment_path}")"
  control_url='https://aiwise.top'
  enrollment_token="${test_token}"
  chown() { return 41; }
  write_bootstrap_environment
}

set +e
write_failure_output="$( (set -e; simulate_environment_write_failure) 2>&1 )"
write_failure_status=$?
set -e
test "${write_failure_status}" -ne 0
if [[ "${write_failure_output}" == *"${test_token}"* ]]; then
  echo '环境文件写入失败时不得输出令牌' >&2
  exit 1
fi
shopt -s nullglob
write_failure_files=("${test_dir}/write-failure"/agent.env.*)
shopt -u nullglob
if ((${#write_failure_files[@]} != 0)); then
  echo '环境文件写入失败时不得残留含令牌的临时文件' >&2
  exit 1
fi
