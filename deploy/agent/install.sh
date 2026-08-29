#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 权限安装云令代理。" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_binary="${1:-${script_dir}/yunling-agent}"
credentials_path="/var/lib/yunling-agent/credentials.json"
legacy_credentials_path="/etc/yunling-agent/credentials.json"
environment_path="/etc/yunling-agent/agent.env"

if [[ ! -f "${source_binary}" ]]; then
  echo "未找到代理程序：${source_binary}" >&2
  exit 1
fi
for required_file in yunling-agent.service yunling-run@.service 50-yunling-agent.rules; do
  if [[ ! -f "${script_dir}/${required_file}" ]]; then
    echo "安装包缺少文件：${required_file}" >&2
    exit 1
  fi
done
if [[ ! -d /etc/polkit-1/rules.d ]]; then
  echo "未检测到 polkit 规则目录，请先安装 polkit。" >&2
  exit 1
fi
if ! command -v systemd >/dev/null 2>&1; then
  echo "未检测到 systemd，无法安装隔离任务模板。" >&2
  exit 1
fi
systemd_version="$(systemd --version | awk 'NR == 1 { print $2 }')"
if [[ ! "${systemd_version}" =~ ^[0-9]+$ ]] || (( systemd_version < 240 )); then
  echo "云令代理要求 systemd 240 或更高版本，当前版本：${systemd_version:-未知}。" >&2
  exit 1
fi

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
install -o root -g root -m 0755 "${source_binary}" /usr/local/bin/yunling-agent
install -o root -g root -m 0644 "${script_dir}/yunling-agent.service" /etc/systemd/system/yunling-agent.service
install -o root -g root -m 0644 "${script_dir}/yunling-run@.service" /etc/systemd/system/yunling-run@.service
install -o root -g root -m 0644 "${script_dir}/50-yunling-agent.rules" /etc/polkit-1/rules.d/50-yunling-agent.rules

if [[ -f "${legacy_credentials_path}" && ! -f "${credentials_path}" ]]; then
  install -o yunling-agent -g yunling-agent -m 0600 "${legacy_credentials_path}" "${credentials_path}"
  rm -f "${legacy_credentials_path}"
fi

umask 077
{
  echo 'YUNLING_CREDENTIALS_PATH=/var/lib/yunling-agent/credentials.json'
  echo 'YUNLING_WORK_DIR=/var/lib/yunling-agent'
  echo 'YUNLING_RUNTIMES=bash,python3'
  if [[ ! -f "${credentials_path}" ]]; then
    : "${YUNLING_CONTROL_URL:?首次安装请通过环境变量提供 YUNLING_CONTROL_URL}"
    : "${YUNLING_ENROLLMENT_TOKEN:?首次安装请通过环境变量提供 YUNLING_ENROLLMENT_TOKEN}"
    if [[ "${YUNLING_CONTROL_URL}" == *$'\n'* || "${YUNLING_ENROLLMENT_TOKEN}" == *$'\n'* ]]; then
      echo "中央服务地址和注册令牌不得包含换行符。" >&2
      exit 1
    fi
    printf 'YUNLING_CONTROL_URL=%s\n' "${YUNLING_CONTROL_URL}"
    printf 'YUNLING_ENROLLMENT_TOKEN=%s\n' "${YUNLING_ENROLLMENT_TOKEN}"
  fi
} >"${environment_path}"
chown root:yunling-agent "${environment_path}"
chmod 0640 "${environment_path}"
if [[ -f "${credentials_path}" ]]; then
  chown yunling-agent:yunling-agent "${credentials_path}"
  chmod 0600 "${credentials_path}"
fi

systemctl daemon-reload
systemctl enable --now yunling-agent.service

if [[ ! -f "${credentials_path}" ]]; then
  for _ in {1..30}; do
    [[ -f "${credentials_path}" ]] && break
    sleep 1
  done
fi

if [[ -f "${credentials_path}" ]]; then
  temporary_environment="$(mktemp /etc/yunling-agent/.agent.env.XXXXXX)"
  grep -v '^YUNLING_\(CONTROL_URL\|ENROLLMENT_TOKEN\)=' "${environment_path}" >"${temporary_environment}"
  chown root:yunling-agent "${temporary_environment}"
  chmod 0640 "${temporary_environment}"
  mv -f "${temporary_environment}" "${environment_path}"
  systemctl restart yunling-agent.service
  echo "云令代理安装完成，已启用 systemd 服务。"
else
  echo "代理在 30 秒内未完成注册，请查看：journalctl -u yunling-agent.service" >&2
  exit 1
fi
