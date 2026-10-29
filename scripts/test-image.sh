#!/usr/bin/env bash
set -euo pipefail
: "${IMAGE:=kubedeploy-controller:dev}"
name="kubedeploy-controller-check-$$-$RANDOM"
volume="$name-state"
work_dir="$(mktemp -d)"
cleanup() {
  result=$?
  if (( result != 0 )); then docker logs "$name" >&2 2>/dev/null || true; fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  rm -rf "$work_dir"
}
trap cleanup EXIT
docker volume create "$volume" >/dev/null
runtime=(--read-only --cap-drop ALL --security-opt no-new-privileges
  --tmpfs '/tmp:rw,noexec,nosuid,size=128m'
  --mount "type=volume,src=$volume,dst=/state")

docker run --rm "${runtime[@]}" --entrypoint bash "$IMAGE" -ceu '
  test "$(id -u):$(id -g)" = 65532:65532
  if touch /etc/controller-write-check 2>/dev/null; then exit 1; fi
  printf persisted > /state/persistence-check
  score-k8s check-version "=0.19.0"
  kubectl version --client -o json | python3 -c '\''import json,sys; v=json.load(sys.stdin)["clientVersion"]; assert v["gitVersion"]=="v1.36.5" and v["goVersion"]=="go1.27.3", v'\''
  version="$(gh --version)"
  [[ "$version" == "gh version 2.103.3 "* ]]
  for tool in bash git make curl python3; do "$tool" --version >/dev/null; done
  /usr/local/bin/deploy-controller --help >/dev/null 2>&1
'
mkdir -p "$work_dir/fixtures"
cat > "$work_dir/fixtures/gh" <<'SH'
#!/bin/sh
[ ! -e /state/fail-poll ] || exit 1
printf '[[]]\n'
SH
chmod 755 "$work_dir/fixtures" "$work_dir/fixtures/gh"
fake_cli=(--mount "type=bind,src=$work_dir/fixtures,dst=/fixtures,readonly"
  --env PATH=/fixtures:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin)
docker run --rm "${runtime[@]}" "${fake_cli[@]}" "$IMAGE" \
  --once --execute --state-dir /state > "$work_dir/result.json"
python3 - "$work_dir/result.json" <<'PY'
import json,sys
with open(sys.argv[1]) as f:
    assert json.load(f) == {"state": "idle"}
PY
docker run --detach --name "$name" "${runtime[@]}" "${fake_cli[@]}" "$IMAGE" \
  --execute --state-dir /state --interval 10s --health-address 127.0.0.1:8081 >/dev/null
probe() {
  docker exec "$name" curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' \
    "http://127.0.0.1:8081/$1" 2>/dev/null || true
}
ready=false
for ((attempt=0; attempt<30; attempt++)); do
  if [[ "$(probe readyz)" == 200 ]]; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]]
[[ "$(probe healthz)" == 200 ]]
docker exec "$name" touch /state/fail-poll
failed=false
for ((attempt=0; attempt<30; attempt++)); do
  if [[ "$(probe readyz)" == 503 ]]; then failed=true; break; fi
  sleep 1
done
[[ "$failed" == true && "$(probe healthz)" == 200 ]]
docker exec "$name" rm /state/fail-poll
ready=false
for ((attempt=0; attempt<30; attempt++)); do
  if [[ "$(probe readyz)" == 200 ]]; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]]
docker exec "$name" sh -c 'test "$(cat /state/persistence-check)" = persisted; test "$(readlink /proc/1/exe)" = /usr/local/bin/deploy-controller'
docker stop --time 5 "$name" >/dev/null
[[ "$(docker wait "$name")" == 0 ]]
echo 'Container smoke passed: tools, non-root, read-only root, persistent state, probes, recovery and SIGTERM'
