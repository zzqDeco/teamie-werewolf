#!/usr/bin/env bash
# Production deployment & rollback executor on Aliyun ECS (/srv/teamie-werewolf).
# Strictly isolated from /srv/teamie (:80/:443) and /srv/drop2empire (:8443).
set -Eeuo pipefail

MODE=${1:-}
BASE_DIR="/srv/teamie-werewolf"
CONFIG_DIR="$BASE_DIR/config"
ENV_FILE="$CONFIG_DIR/production.env"
TLS_DIR="$BASE_DIR/tls"
ARCHIVES_DIR="$BASE_DIR/archives"
LOCK_FILE="$BASE_DIR/.deploy.lock"

mkdir -p "$CONFIG_DIR" "$TLS_DIR" "$ARCHIVES_DIR" "$BASE_DIR/incoming" "$BASE_DIR/releases"
chmod 0700 "$CONFIG_DIR"
chmod 0755 "$BASE_DIR" "$TLS_DIR"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "Another teamie-werewolf deployment is currently in progress." >&2
  exit 1
fi

# Read optional JSON registry auth payload from stdin
AUTH_JSON=""
if [[ ! -t 0 ]]; then
  AUTH_JSON=$(cat || true)
fi

docker_login_if_needed() {
  local image_ref="$1"
  local registry="${image_ref%%/*}"
  if [[ -z "$AUTH_JSON" ]]; then
    return 0
  fi
  if [[ "$registry" == *.aliyuncs.com ]]; then
    local user pass
    user=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1] or "{}"); print(d.get("acr",{}).get("username",""))' "$AUTH_JSON")
    pass=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1] or "{}"); print(d.get("acr",{}).get("password",""))' "$AUTH_JSON")
    if [[ -n "$user" && -n "$pass" ]]; then
      printf '%s' "$pass" | docker login "$registry" --username "$user" --password-stdin >/dev/null
    fi
  elif [[ "$registry" == "ghcr.io" ]]; then
    local user pass
    user=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1] or "{}"); print(d.get("ghcr",{}).get("username",""))' "$AUTH_JSON")
    pass=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1] or "{}"); print(d.get("ghcr",{}).get("password",""))' "$AUTH_JSON")
    if [[ -n "$user" && -n "$pass" ]]; then
      printf '%s' "$pass" | docker login ghcr.io --username "$user" --password-stdin >/dev/null
    fi
  fi
}

ensure_env_file() {
  if [[ ! -f "$ENV_FILE" ]]; then
    local boot_sec
    boot_sec="tmsec_bootstrap_$(openssl rand -hex 20)"
    umask 077
    cat > "$ENV_FILE" <<EOF
PUBLIC_HTTPS_PORT=9443
TLS_CERT_DIR=/srv/teamie-werewolf/tls
TEAMIE_ORIGIN=https://120.25.210.202
PHASE_TIMEOUT_SEC=90
TEAMIE_APP_ID=00000000-0000-4000-8000-bootstrap0001
TEAMIE_APP_SIGNING_SECRET=${boot_sec}
EOF
  fi
}

verify_port_safe() {
  local port="$1"
  if ss -tln | awk '{print $4}' | grep -Eq "(^|:)${port}$"; then
    if ! docker ps --filter "label=com.docker.compose.project=teamie-werewolf" --format '{{.Ports}}' | grep -q ":${port}->"; then
      echo "ERROR: Port ${port} is occupied by a non-teamie-werewolf service! Aborting." >&2
      exit 1
    fi
  fi
}

probe_stack() {
  local port="$1"
  local expected_ver="$2"
  local attempt
  for attempt in $(seq 1 25); do
    if curl -ksSf --max-time 3 "https://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
      local ver_json html_ct mcp_code
      ver_json=$(curl -ksSf --max-time 3 "https://127.0.0.1:${port}/version" || true)
      html_ct=$(curl -ksSI --max-time 3 "https://127.0.0.1:${port}/" | tr -d '\r' | awk -F': ' 'tolower($1)=="content-type"{print tolower($2)}')
      mcp_code=$(curl -ksS -o /dev/null -w '%{http_code}' --max-time 3 -X POST -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"initialize"}' "https://127.0.0.1:${port}/mcp" || true)
      if [[ "$html_ct" == *"text/html"* && "$mcp_code" == "401" ]]; then
        echo "Verified live HTTPS stack on :${port} (HTML=${html_ct}, MCP unauth=${mcp_code}, version=${ver_json})"
        return 0
      fi
    fi
    sleep 2
  done
  echo "ERROR: Post-deploy HTTPS verification failed on port ${port} for ${expected_ver}" >&2
  return 1
}

rollout_release_dir() {
  local rel_dir="$1"
  eval "$(python3 "$rel_dir/ops/manifest.py" verify --manifest "$rel_dir/release-manifest.json" --print-env)"

  ensure_env_file
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  local port="${PUBLIC_HTTPS_PORT:-9443}"
  verify_port_safe "$port"

  bash "$rel_dir/ops/sync-tls-cert.sh" "${PUBLIC_HOST_IP:-120.25.210.202}"

  # Load bundled image tarball if included (prevents Mainland China GHCR/DockerHub network timeouts)
  if [[ -f "$rel_dir/werewolf-image.tar.gz" ]]; then
    gzip -dc "$rel_dir/werewolf-image.tar.gz" | docker load >/dev/null
    rm -f "$rel_dir/werewolf-image.tar.gz"
  elif ! docker image inspect "$WEREWOLF_IMAGE" >/dev/null 2>&1; then
    docker_login_if_needed "$WEREWOLF_IMAGE"
    docker pull "$WEREWOLF_IMAGE" >/dev/null
  fi

  if ! docker image inspect "$CADDY_IMAGE" >/dev/null 2>&1; then
    if [[ "$CADDY_IMAGE" == "teamie-werewolf-caddy:local" ]] && docker ps --format '{{.Names}}' | grep -qx 'teamie-caddy-1'; then
      local caddy_id
      caddy_id=$(docker inspect teamie-caddy-1 --format '{{.Image}}')
      docker tag "$caddy_id" "teamie-werewolf-caddy:local"
    else
      docker_login_if_needed "$CADDY_IMAGE"
      docker pull "$CADDY_IMAGE" >/dev/null
    fi
  fi

  cat > "$rel_dir/deploy/images.env" <<EOF
WEREWOLF_IMAGE=${WEREWOLF_IMAGE}
CADDY_IMAGE=${CADDY_IMAGE}
EOF

  docker compose \
    --project-name teamie-werewolf \
    --project-directory "$rel_dir/deploy" \
    --env-file "$ENV_FILE" \
    --env-file "$rel_dir/deploy/images.env" \
    -f "$rel_dir/deploy/compose.production.yaml" \
    up -d --remove-orphans

  probe_stack "$port" "$RELEASE_VERSION"
}

if [[ "$MODE" == "apply" ]]; then
  TARGET_DIR=${2:?release directory required}
  PREV_DIR=""
  if [[ -L "$BASE_DIR/current" ]]; then
    PREV_DIR=$(readlink -f "$BASE_DIR/current" || true)
  fi

  if rollout_release_dir "$TARGET_DIR"; then
    eval "$(python3 "$TARGET_DIR/ops/manifest.py" verify --manifest "$TARGET_DIR/release-manifest.json" --print-env)"
    ln -sfn "$TARGET_DIR" "$BASE_DIR/current"
    ln -sfn "$TARGET_DIR" "$ARCHIVES_DIR/$RELEASE_VERSION"
    echo "Successfully deployed teamie-werewolf $RELEASE_VERSION ($RELEASE_SHA)"
  else
    if [[ -n "$PREV_DIR" && -d "$PREV_DIR" ]]; then
      echo "Rolling back automatically to previous release: $PREV_DIR" >&2
      rollout_release_dir "$PREV_DIR" || true
    fi
    exit 1
  fi

elif [[ "$MODE" == "rollback" ]]; then
  ROLLBACK_VERSION=${2:?rollback version required}
  EXPECTED_CURRENT_SHA=${3:?expected current sha required}

  if [[ ! -L "$BASE_DIR/current" ]]; then
    echo "No active current deployment symlink found." >&2
    exit 1
  fi
  CURRENT_DIR=$(readlink -f "$BASE_DIR/current")
  CURRENT_SHA=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["sha"])' "$CURRENT_DIR/release-manifest.json")
  if [[ "$CURRENT_SHA" != "$EXPECTED_CURRENT_SHA" ]]; then
    echo "Current SHA mismatch: expected $EXPECTED_CURRENT_SHA, got $CURRENT_SHA" >&2
    exit 1
  fi

  ARCHIVE_TARGET=$(readlink -f "$ARCHIVES_DIR/$ROLLBACK_VERSION" || true)
  if [[ -z "$ARCHIVE_TARGET" || ! -d "$ARCHIVE_TARGET" ]]; then
    echo "Archived version $ROLLBACK_VERSION not found in $ARCHIVES_DIR" >&2
    exit 1
  fi

  rollout_release_dir "$ARCHIVE_TARGET"
  ln -sfn "$ARCHIVE_TARGET" "$BASE_DIR/current"
  echo "Successfully rolled back teamie-werewolf to $ROLLBACK_VERSION"
else
  echo "Usage: deploy.sh <apply|rollback> ..." >&2
  exit 1
fi
