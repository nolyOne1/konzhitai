#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 权限安装云令代理。" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_binary="${1:-${script_dir}/yunling-agent}"
credentials_path="/etc/yunling-agent/credentials.json"
environment_path="/etc/yunling-agent/agent.env"

if [[ ! -f "${source_binary}" ]]; then
  echo "未找到代理程序：${source_binary}" >&2
  exit 1
fi

if ! id yunling-runner >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/yunling-agent --shell /usr/sbin/nologin yunling-runner
fi

install -d -o root -g yunling-runner -m 0750 /etc/yunling-agent
install -d -o root -g yunling-runner -m 0750 /var/lib/yunling-agent
install -d -o root -g yunling-runner -m 0750 /var/lib/yunling-agent/script-cache
install -d -o root -g yunling-runner -m 0750 /var/lib/yunling-agent/runs
install -o root -g root -m 0755 "${source_binary}" /usr/local/bin/yunling-agent
install -o root -g root -m 0644 "${script_dir}/yunling-agent.service" /etc/systemd/system/yunling-agent.service

umask 077
{
  echo 'YUNLING_CREDENTIALS_PATH=/etc/yunling-agent/credentials.json'
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
chmod 0600 "${environment_path}"

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
  chmod 0600 "${temporary_environment}"
  mv -f "${temporary_environment}" "${environment_path}"
  echo "云令代理安装完成，已启用 systemd 服务。"
else
  echo "代理在 30 秒内未完成注册，请查看：journalctl -u yunling-agent.service" >&2
  exit 1
fi
