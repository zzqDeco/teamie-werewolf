#!/usr/bin/env bash
# GitHub Actions hosted-runner deployment client (aligned with zzqDeco/teamie/ops/ci-deploy.sh).
# Secrets are passed strictly through environment/stdin, never command-line arguments.
set -euo pipefail
umask 077

mode=${1:-apply}
[[ $mode == apply || $mode == rollback ]] || exit 1
: "${PRODUCTION_SSH_HOST:?}" "${PRODUCTION_SSH_USER:?}" "${PRODUCTION_SSH_KEY:?}" "${PRODUCTION_SSH_KNOWN_HOSTS:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_RUN_ATTEMPT:?}"
[[ $PRODUCTION_SSH_HOST =~ ^[A-Za-z0-9.-]+$ && $PRODUCTION_SSH_USER =~ ^[a-z][a-z0-9_-]*$ && $GITHUB_RUN_ID =~ ^[0-9]+$ && $GITHUB_RUN_ATTEMPT =~ ^[0-9]+$ ]] || exit 1

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
printf '%s\n' "$PRODUCTION_SSH_KEY" > "$scratch/key"
printf '%s\n' "$PRODUCTION_SSH_KNOWN_HOSTS" > "$scratch/known_hosts"
unset PRODUCTION_SSH_KEY PRODUCTION_SSH_KNOWN_HOSTS

ssh_opts=(
  -i "$scratch/key"
  -o BatchMode=yes
  -o IdentitiesOnly=yes
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$scratch/known_hosts"
  -o ConnectTimeout=15
  -o ServerAliveInterval=30
  -o ServerAliveCountMax=10
)
remote="$PRODUCTION_SSH_USER@$PRODUCTION_SSH_HOST"

if [[ $mode == apply ]]; then
  : "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}"
  [[ $RELEASE_VERSION =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $RELEASE_SHA =~ ^[0-9a-f]{40}$ ]] || exit 1
  name="$RELEASE_VERSION-$RELEASE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  bundle_items=(deploy ops release-manifest.json)
  if [[ -f werewolf-image.tar.gz ]]; then
    bundle_items+=(werewolf-image.tar.gz)
  fi
  tar -czf "$scratch/bundle.tgz" "${bundle_items[@]}"
  ssh "${ssh_opts[@]}" "$remote" "mkdir -p /srv/teamie-werewolf/incoming /srv/teamie-werewolf/releases"
  scp "${ssh_opts[@]}" "$scratch/bundle.tgz" "$remote:/srv/teamie-werewolf/incoming/$name.tgz"
  command="umask 022 && mkdir '/srv/teamie-werewolf/releases/$name' && tar -xzf '/srv/teamie-werewolf/incoming/$name.tgz' --no-same-owner --no-same-permissions -C '/srv/teamie-werewolf/releases/$name' && rm '/srv/teamie-werewolf/incoming/$name.tgz' && bash '/srv/teamie-werewolf/releases/$name/ops/deploy.sh' apply '/srv/teamie-werewolf/releases/$name'"
else
  : "${ROLLBACK_VERSION:?}" "${EXPECTED_CURRENT_SHA:?}"
  [[ $ROLLBACK_VERSION =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $EXPECTED_CURRENT_SHA =~ ^[0-9a-f]{40}$ ]] || exit 1
  name="rollback-$ROLLBACK_VERSION-$EXPECTED_CURRENT_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  tar -czf "$scratch/bundle.tgz" ops
  ssh "${ssh_opts[@]}" "$remote" "mkdir -p /srv/teamie-werewolf/incoming /srv/teamie-werewolf/releases"
  scp "${ssh_opts[@]}" "$scratch/bundle.tgz" "$remote:/srv/teamie-werewolf/incoming/$name.tgz"
  command="umask 022 && mkdir '/srv/teamie-werewolf/releases/$name' && tar -xzf '/srv/teamie-werewolf/incoming/$name.tgz' --no-same-owner --no-same-permissions -C '/srv/teamie-werewolf/releases/$name' && rm '/srv/teamie-werewolf/incoming/$name.tgz' && bash '/srv/teamie-werewolf/releases/$name/ops/deploy.sh' rollback '$ROLLBACK_VERSION' '$EXPECTED_CURRENT_SHA'"
fi

# shellcheck disable=SC2029
python3 - <<'PYAUTH' | ssh "${ssh_opts[@]}" "$remote" "$command"
import json, os, sys
json.dump({
    "acr": {
        "username": os.environ.get("REGISTRY_USERNAME", ""),
        "password": os.environ.get("REGISTRY_PASSWORD", ""),
    },
    "ghcr": {
        "username": os.environ.get("GHCR_USERNAME", ""),
        "password": os.environ.get("GHCR_TOKEN", ""),
    },
}, sys.stdout)
PYAUTH
