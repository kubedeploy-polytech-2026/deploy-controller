#!/usr/bin/env bash
set -euo pipefail
umask 077
mkdir -p "$HOME" "$XDG_CACHE_HOME" "$GH_CONFIG_DIR" "$DOCKER_CONFIG"
exec /usr/local/bin/deploy-controller "$@"
