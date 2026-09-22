#!/usr/bin/env bash
# Only for a disposable GitHub-hosted VM. Never run on an enrolled node.
set -euo pipefail

[[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ]] || {
  echo 'Requires a disposable GitHub-hosted runner.' >&2; exit 1;
}
[[ "$(uname -s)" == Linux && "$(id -u)" == 0 && "$(cat /proc/1/comm)" == systemd ]]
[[ $# == 1 && -n "${RUNNER_TEMP:-}" ]]
build_dir="$(realpath -e -- "$1")"
runner_temp="$(realpath -e -- "$RUNNER_TEMP")"
[[ "$build_dir" == "$runner_temp/yunling-systemd-ci" ]]
[[ -x "$build_dir/yunling-agent" && -x "$build_dir/executor.test" ]]
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Refuse all existing installations/accounts rather than overwriting them.
for path in /var/lib/yunling-agent /etc/yunling-agent /usr/local/bin/yunling-agent \
  /usr/local/libexec/yunling-systemd-ci.test \
  /etc/systemd/system/yunling-agent.service /etc/systemd/system/yunling-run@.service \
  /etc/polkit-1/rules.d/50-yunling-agent.rules; do
  [[ ! -e "$path" && ! -L "$path" ]] || { echo "Existing path: $path" >&2; exit 1; }
done
for account in yunling-agent yunling-runner; do
  ! getent passwd "$account" >/dev/null || exit 1
  ! getent group "$account" >/dev/null || exit 1
done
[[ -z "$(systemctl list-units --all --no-legend 'yunling-*.service')" ]]
[[ -z "$(systemctl list-unit-files --no-legend 'yunling-*.service')" ]]
systemctl --version
systemctl start polkit.service

groupadd --system yunling-runner
useradd --system --gid yunling-runner --no-create-home --shell /usr/sbin/nologin yunling-runner
groupadd --system yunling-agent
useradd --system --gid yunling-agent --groups yunling-runner --no-create-home --shell /usr/sbin/nologin yunling-agent
install -d -o yunling-agent -g yunling-runner -m 2750 \
  /var/lib/yunling-agent /var/lib/yunling-agent/runs \
  /var/lib/yunling-agent/script-cache /var/lib/yunling-agent/script-cache/scripts
install -d -o root -g root -m 0755 /usr/local/libexec
install -o root -g root -m 0755 "$build_dir/yunling-agent" /usr/local/bin/yunling-agent
install -o root -g root -m 0755 "$build_dir/executor.test" /usr/local/libexec/yunling-systemd-ci.test
install -o root -g root -m 0644 "$script_dir/yunling-run@.service" /etc/systemd/system/yunling-run@.service
install -o root -g root -m 0644 "$script_dir/50-yunling-agent.rules" /etc/polkit-1/rules.d/50-yunling-agent.rules
systemctl daemon-reload
systemctl restart polkit.service
systemd-analyze verify /etc/systemd/system/yunling-run@.service

# No credentials, enrollment, agent daemon or control-plane connection.
# Clear the workflow environment; tests run as the real unprivileged agent.
umask 0007
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 \
  /usr/local/libexec/yunling-systemd-ci.test -test.list '^TestSystemdIsolatedAcceptance$' \
  | grep -Fx 'TestSystemdIsolatedAcceptance'
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 \
  /usr/local/libexec/yunling-systemd-ci.test \
  -test.run '^TestSystemdIsolatedAcceptance$' -test.count=1 -test.timeout=3m -test.v
# The entire disposable VM is discarded by GitHub after the job; do not add
# recursive cleanup commands that could make this script dangerous elsewhere.
