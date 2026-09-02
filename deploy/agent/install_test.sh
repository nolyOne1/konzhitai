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
  '检测到现有云令代理身份，正在修复安装并保留原节点身份。' \
  '/usr/local/bin/.yunling-agent.new' \
  'getent' 'groupadd' 'useradd' 'usermod' 'install' 'systemctl' 'mktemp'; do
  grep -Fq "${required_text}" "${installer}"
done
if grep -Fq '! -t 0' "${installer}"; then
  echo '安装器从 /dev/tty 读取令牌时不得要求 heredoc 标准输入也是终端' >&2
  exit 1
fi
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

simulate_post_registration_failure() {
  environment_path="${test_dir}/registered/agent.env"
  credentials_path="${test_dir}/registered/credentials.json"
  mkdir -p "$(dirname -- "${environment_path}")"
  printf 'A=1\nYUNLING_CONTROL_URL=%s\nYUNLING_ENROLLMENT_TOKEN=%s\nB=2\n' \
    'https://aiwise.top' "${test_token}" >"${environment_path}"
  printf '{}\n' >"${credentials_path}"
  registration_complete=1
  install_complete=0
  service_started=1
  enrollment_token="${test_token}"
  control_url='https://aiwise.top'
  systemctl() { printf '%s\n' "$*" >>"${cleanup_log}"; }
  chown() { return 0; }
  chmod() { return 0; }
  trap cleanup_install EXIT
  return 24
}

registered_stop_count_before="$(grep -Fc 'stop yunling-agent.service' "${cleanup_log}" || true)"
set +e
registered_cleanup_output="$( (simulate_post_registration_failure) 2>&1 )"
registered_cleanup_status=$?
set -e
test "${registered_cleanup_status}" -eq 24
test "$(<"${test_dir}/registered/agent.env")" = $'A=1\nB=2'
if [[ "${registered_cleanup_output}" == *"${test_token}"* ]]; then
  echo '注册后失败清理输出不得包含令牌' >&2
  exit 1
fi
registered_stop_count_after="$(grep -Fc 'stop yunling-agent.service' "${cleanup_log}" || true)"
if [[ "${registered_stop_count_after}" != "${registered_stop_count_before}" ]]; then
  echo '注册完成后失败不得停止代理服务' >&2
  exit 1
fi
grep -Fq 'restart yunling-agent.service' "${cleanup_log}"

simulate_existing_identity_repair() {
  environment_path="${test_dir}/repair/agent.env"
  credentials_path="${test_dir}/repair/credentials.json"
  legacy_credentials_path="${test_dir}/repair/legacy-credentials.json"
  mkdir -p "$(dirname -- "${environment_path}")"
  printf 'A=1\nYUNLING_CONTROL_URL=%s\nYUNLING_ENROLLMENT_TOKEN=%s\nB=2\n' \
    'https://aiwise.top' "${test_token}" >"${environment_path}"
  printf '{"existing":true}\n' >"${credentials_path}"
  check_preflight() { return 0; }
  install_files() { printf 'install-files\n' >>"${test_dir}/repair.log"; }
  read_enrollment_token() { printf 'unexpected-token-read\n' >>"${test_dir}/repair.log"; return 99; }
  chown() { return 0; }
  chmod() { return 0; }
  systemctl() { printf '%s\n' "$*" >>"${test_dir}/repair.log"; }
  main --control-url 'https://aiwise.top'
}

repair_output="$( (simulate_existing_identity_repair) 2>&1 )"
test "$(<"${test_dir}/repair/credentials.json")" = '{"existing":true}'
test "$(<"${test_dir}/repair/agent.env")" = $'A=1\nB=2'
grep -Fq '检测到现有云令代理身份，正在修复安装并保留原节点身份。' <<<"${repair_output}"
grep -Fq '云令代理安装已修复，原节点身份保持不变。' <<<"${repair_output}"
grep -Fq 'enable --now yunling-agent.service' "${test_dir}/repair.log"
grep -Fq 'restart yunling-agent.service' "${test_dir}/repair.log"
if grep -Fq 'unexpected-token-read' "${test_dir}/repair.log"; then
  echo '修复现有身份时不得读取或消费新令牌' >&2
  exit 1
fi
