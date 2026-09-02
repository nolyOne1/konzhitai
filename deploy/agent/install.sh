#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_binary="${script_dir}/yunling-agent"
credentials_path="/var/lib/yunling-agent/credentials.json"
legacy_credentials_path="/etc/yunling-agent/credentials.json"
environment_path="/etc/yunling-agent/agent.env"
control_url=''
enrollment_token=''
registration_complete=0
service_started=0
install_complete=0

usage() {
  echo '用法：install.sh --control-url https://控制平面域名' >&2
}

validate_control_url() {
  local value="${1:-}"
  local port=''
  if [[ ! "${value}" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$ ]]; then
    return 1
  fi
  if [[ "${value}" =~ :([0-9]{1,5})$ ]]; then
    port="${BASH_REMATCH[1]}"
    if ((10#${port} < 1 || 10#${port} > 65535)); then
      return 1
    fi
  fi
}

parse_args() {
  if [[ "$#" -ne 2 || "${1:-}" != '--control-url' ]]; then
    usage
    return 1
  fi
  if ! validate_control_url "${2}"; then
    echo '控制平面地址必须是仅包含 HTTPS 来源的地址，例如 https://aiwise.top。' >&2
    return 1
  fi
  control_url="${2}"
}

strip_bootstrap_lines() {
  local content="${1:-}"
  local line=''
  while IFS= read -r line; do
    case "${line}" in
      YUNLING_CONTROL_URL=*|YUNLING_ENROLLMENT_TOKEN=*) continue ;;
    esac
    printf '%s\n' "${line}"
  done <<<"${content}"
}

check_preflight() {
  local failed=0
  local command_name=''
  local systemd_output=''
  local systemd_version=''

  if ((EUID != 0)); then
    echo '请使用 root 权限安装云令代理。' >&2
    failed=1
  fi
  if [[ ! -r /dev/tty || ! -w /dev/tty ]]; then
    echo '需要可读写的交互式终端，请在 SSH 或网页终端中重新运行。' >&2
    failed=1
  fi
  if [[ ! -d /etc/polkit-1/rules.d ]]; then
    echo '未检测到 polkit 规则目录 /etc/polkit-1/rules.d。' >&2
    failed=1
  fi
  for command_name in systemd getent id groupadd useradd usermod install systemctl mktemp chown chmod mv rm sleep; do
    if ! command -v "${command_name}" >/dev/null 2>&1; then
      echo "缺少安装依赖命令：${command_name}" >&2
      failed=1
    fi
  done
  if command -v systemd >/dev/null 2>&1; then
    systemd_output="$(systemd --version 2>/dev/null || true)"
    systemd_version="${systemd_output#systemd }"
    systemd_version="${systemd_version%%[!0-9]*}"
    if [[ ! "${systemd_version}" =~ ^[0-9]+$ ]] || ((systemd_version < 240)); then
      echo "云令代理要求 systemd 240 或更高版本，当前版本：${systemd_version:-未知}。" >&2
      failed=1
    fi
  fi
  if [[ ! -f "${source_binary}" || -L "${source_binary}" || ! -x "${source_binary}" ]]; then
    echo '安装包中的 yunling-agent 缺失、不是普通文件或不可执行。' >&2
    failed=1
  fi
  for command_name in yunling-agent.service yunling-run@.service 50-yunling-agent.rules; do
    if [[ ! -f "${script_dir}/${command_name}" || -L "${script_dir}/${command_name}" ]]; then
      echo "安装包缺少普通文件：${command_name}" >&2
      failed=1
    fi
  done
  if ((failed != 0)); then
    echo '请安装或修复上述依赖后重新执行原命令；安装器不会自动修改系统软件。' >&2
    return 1
  fi
}

read_enrollment_token() {
  IFS= read -r -s -p '请输入一次性注册令牌：' enrollment_token </dev/tty
  printf '\n' >/dev/tty
  if [[ -z "${enrollment_token}" ]]; then
    echo '注册令牌不能为空。' >&2
    return 1
  fi
  if [[ ! "${enrollment_token}" =~ ^[A-Za-z0-9_-]{20,256}$ ]]; then
    enrollment_token=''
    echo '注册令牌格式无效，请从云令控制台重新复制。' >&2
    return 1
  fi
}

strip_bootstrap_file() {
  local temporary_environment=''
  if [[ ! -f "${environment_path}" ]]; then
    return 0
  fi
  temporary_environment="$(mktemp "${environment_path}.XXXXXX")" || return 1
  if ! strip_bootstrap_lines "$(<"${environment_path}")" >"${temporary_environment}" ||
    ! chown root:yunling-agent "${temporary_environment}" ||
    ! chmod 0640 "${temporary_environment}" ||
    ! mv -f "${temporary_environment}" "${environment_path}"; then
    rm -f "${temporary_environment}" >/dev/null 2>&1 || true
    return 1
  fi
}

cleanup_install() {
  local status=$?
  trap - EXIT
  enrollment_token=''
  control_url=''
  if ((registration_complete == 0)); then
    if ((service_started == 1)); then
      systemctl stop yunling-agent.service >/dev/null 2>&1 || true
    fi
  fi
  if ! strip_bootstrap_file; then
    rm -f "${environment_path}" >/dev/null 2>&1 || true
  fi
  if ((registration_complete == 1 && install_complete == 0 && service_started == 1)); then
    systemctl restart yunling-agent.service >/dev/null 2>&1 || true
  fi
  exit "${status}"
}

install_files() {
  local temporary_binary='/usr/local/bin/.yunling-agent.new'
  if ! getent group yunling-runner >/dev/null 2>&1; then
    groupadd --system yunling-runner
  fi
  if ! id yunling-runner >/dev/null 2>&1; then
    useradd --system --gid yunling-runner --no-create-home --home-dir /var/lib/yunling-agent --shell /usr/sbin/nologin yunling-runner
  fi
  if ! getent group yunling-agent >/dev/null 2>&1; then
    groupadd --system yunling-agent
  fi
  if ! id yunling-agent >/dev/null 2>&1; then
    useradd --system --gid yunling-agent --no-create-home --home-dir /var/lib/yunling-agent --shell /usr/sbin/nologin yunling-agent
  fi
  usermod --append --groups yunling-runner yunling-agent

  install -d -o root -g yunling-agent -m 0750 /etc/yunling-agent
  install -d -o yunling-agent -g yunling-runner -m 2750 /var/lib/yunling-agent
  install -d -o yunling-agent -g yunling-runner -m 2750 /var/lib/yunling-agent/script-cache
  install -d -o yunling-agent -g yunling-runner -m 2750 /var/lib/yunling-agent/runs
  rm -f "${temporary_binary}"
  install -o root -g root -m 0755 "${source_binary}" "${temporary_binary}"
  if ! mv -f "${temporary_binary}" /usr/local/bin/yunling-agent; then
    rm -f "${temporary_binary}" >/dev/null 2>&1 || true
    return 1
  fi
  install -o root -g root -m 0644 "${script_dir}/yunling-agent.service" /etc/systemd/system/yunling-agent.service
  install -o root -g root -m 0644 "${script_dir}/yunling-run@.service" /etc/systemd/system/yunling-run@.service
  install -o root -g root -m 0644 "${script_dir}/50-yunling-agent.rules" /etc/polkit-1/rules.d/50-yunling-agent.rules
}

write_bootstrap_environment() {
  local temporary_environment=''
  temporary_environment="$(mktemp "${environment_path}.XXXXXX")"
  umask 077
  if ! {
    {
      echo 'YUNLING_CREDENTIALS_PATH=/var/lib/yunling-agent/credentials.json'
      echo 'YUNLING_WORK_DIR=/var/lib/yunling-agent'
      echo 'YUNLING_RUNTIMES=bash,python3'
      printf 'YUNLING_CONTROL_URL=%s\n' "${control_url}"
      printf 'YUNLING_ENROLLMENT_TOKEN=%s\n' "${enrollment_token}"
    } >"${temporary_environment}"
  }; then
    rm -f "${temporary_environment}" >/dev/null 2>&1 || true
    return 1
  fi
  if ! chown root:root "${temporary_environment}" ||
    ! chmod 0600 "${temporary_environment}" ||
    ! mv -f "${temporary_environment}" "${environment_path}"; then
    rm -f "${temporary_environment}" >/dev/null 2>&1 || true
    return 1
  fi
}

wait_for_registration() {
  local attempt=0
  for ((attempt = 0; attempt < 30; attempt++)); do
    if [[ -f "${credentials_path}" ]]; then
      return 0
    fi
    sleep 1
  done
  echo '代理在 30 秒内未完成注册，请查看：journalctl -u yunling-agent.service' >&2
  return 1
}

main() {
  unset YUNLING_ENROLLMENT_TOKEN YUNLING_CONTROL_URL
  parse_args "$@"
  check_preflight
  if [[ -L "${credentials_path}" || (-e "${credentials_path}" && ! -f "${credentials_path}") || -e "${legacy_credentials_path}" || -L "${legacy_credentials_path}" ]]; then
    echo '检测到不受支持的云令代理身份文件，请先在控制台确认后再处理。' >&2
    return 2
  fi

  trap cleanup_install EXIT
  if [[ -f "${credentials_path}" ]]; then
    registration_complete=1
    echo '检测到现有云令代理身份，正在修复安装并保留原节点身份。'
    install_files
    chown yunling-agent:yunling-agent "${credentials_path}"
    chmod 0600 "${credentials_path}"
    strip_bootstrap_file
    systemctl daemon-reload
    service_started=1
    systemctl enable --now yunling-agent.service
    systemctl restart yunling-agent.service
    install_complete=1
    echo '云令代理安装已修复，原节点身份保持不变。'
    return 0
  fi

  read_enrollment_token
  install_files
  write_bootstrap_environment
  systemctl daemon-reload
  service_started=1
  systemctl enable --now yunling-agent.service
  wait_for_registration
  registration_complete=1

  chown yunling-agent:yunling-agent "${credentials_path}"
  chmod 0600 "${credentials_path}"
  strip_bootstrap_file
  enrollment_token=''
  systemctl restart yunling-agent.service
  install_complete=1
  echo '云令代理安装完成，已启用 systemd 服务。'
}

if [[ "${YUNLING_INSTALL_TESTING:-0}" != 1 ]]; then
  main "$@"
fi
