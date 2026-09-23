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
  /etc/systemd/system/yunling-agent.service.d \
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
install -o root -g root -m 0644 "$script_dir/yunling-agent.service" /etc/systemd/system/yunling-agent.service
install -o root -g root -m 0644 "$script_dir/yunling-run@.service" /etc/systemd/system/yunling-run@.service
install -o root -g root -m 0644 "$script_dir/50-yunling-agent.rules" /etc/polkit-1/rules.d/50-yunling-agent.rules
# Keep the parent service alive without credentials or a control-plane connection.
install -d -o root -g root -m 0755 /etc/systemd/system/yunling-agent.service.d
printf '%s\n' '[Service]' 'ExecStart=' 'ExecStart=/usr/bin/sleep infinity' \
  >/etc/systemd/system/yunling-agent.service.d/ci.conf
systemctl daemon-reload
systemctl restart polkit.service
systemctl start yunling-agent.service
systemd-analyze verify /etc/systemd/system/yunling-agent.service /etc/systemd/system/yunling-run@.service

# No credentials, enrollment, agent daemon or control-plane connection.
# Clear the workflow environment; tests run as the real unprivileged agent.
umask 0007
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 \
  /usr/local/libexec/yunling-systemd-ci.test -test.list '^TestSystemdIsolatedAcceptance$' \
  | grep -Fx 'TestSystemdIsolatedAcceptance'
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 \
  /usr/local/libexec/yunling-systemd-ci.test \
  -test.run '^TestSystemdIsolatedAcceptance$' -test.count=1 -test.timeout=3m -test.v

# Recovery report may be authoritative only if no isolated task unit exists.
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 YUNLING_CI_EXPECT_EMPTY=1 \
  /usr/local/libexec/yunling-systemd-ci.test -test.run '^TestSystemdRecoveryProbe$' -test.count=1 -test.v

restart_work=/var/lib/yunling-agent/runs/ci-restart-boundary
restart_unit=yunling-run@ci-restart-boundary.service
install -d -o yunling-runner -g yunling-runner -m 0700 "$restart_work"
printf '%s\n' '{"arguments":["/bin/bash","-c","sleep 300 & echo $! > child.pid; wait"],"environment":{},"working_directory":"/var/lib/yunling-agent/runs/ci-restart-boundary"}' \
  >"$restart_work/systemd-run-spec.json"
chown yunling-runner:yunling-runner "$restart_work/systemd-run-spec.json"
chmod 0640 "$restart_work/systemd-run-spec.json"
systemctl start --no-block "$restart_unit"
for attempt in {1..100}; do
  [[ -s "$restart_work/child.pid" ]] && break
  sleep 0.1
done
[[ -s "$restart_work/child.pid" ]]
child_pid="$(<"$restart_work/child.pid")"
[[ "$child_pid" =~ ^[0-9]+$ ]]
systemctl is-active --quiet "$restart_unit"
runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 YUNLING_CI_EXPECT_EMPTY=0 \
  /usr/local/libexec/yunling-systemd-ci.test -test.run '^TestSystemdRecoveryProbe$' -test.count=1 -test.v
systemctl restart yunling-agent.service
for attempt in {1..100}; do
  if ! systemctl is-active --quiet "$restart_unit"; then break; fi
  sleep 0.1
done
! systemctl is-active --quiet "$restart_unit"
[[ ! -e "/proc/$child_pid/exe" ]]
if ! runuser -u yunling-agent -- env -i PATH=/usr/bin:/bin YUNLING_TEST_SYSTEMD=1 \
  YUNLING_CI_EXPECT_EMPTY=1 YUNLING_CI_WAIT_EMPTY=1 \
  /usr/local/libexec/yunling-systemd-ci.test -test.run '^TestSystemdRecoveryProbe$' -test.count=1 -test.v; then
  systemctl list-units --all --no-pager 'yunling-run@*.service' || true
  systemctl list-jobs --no-pager || true
  systemctl show "$restart_unit" -p ActiveState -p SubState -p Result || true
  exit 1
fi
# The entire disposable VM is discarded by GitHub after the job; do not add
# recursive cleanup commands that could make this script dangerous elsewhere.
