#!/usr/bin/env bash
# Safely updates TEAMIE_APP_ID and TEAMIE_APP_SIGNING_SECRET in /srv/teamie-werewolf/config/production.env
# on the production host via stdin (never stored in Git or shell history) and recreates the werewolf container.
set -euo pipefail

SSH_TARGET=${1:-root@120.25.210.202}
: "${TEAMIE_APP_ID:?Set TEAMIE_APP_ID in environment}"
: "${TEAMIE_APP_SIGNING_SECRET:?Set TEAMIE_APP_SIGNING_SECRET in environment}"

REMOTE_SCRIPT='
import json, os, pathlib, subprocess, sys
creds = json.load(sys.stdin)
env_path = pathlib.Path("/srv/teamie-werewolf/config/production.env")
lines = env_path.read_text(encoding="utf-8").splitlines() if env_path.exists() else []
filtered = [
    ln for ln in lines
    if not ln.startswith("TEAMIE_APP_ID=") and not ln.startswith("TEAMIE_APP_SIGNING_SECRET=")
]
filtered.append("TEAMIE_APP_ID=" + creds["app_id"])
filtered.append("TEAMIE_APP_SIGNING_SECRET=" + creds["signing_secret"])
os.umask(0o077)
env_path.write_text("\n".join(filtered) + "\n", encoding="utf-8")

cur = pathlib.Path("/srv/teamie-werewolf/current").resolve()
subprocess.run([
    "docker", "compose",
    "--project-name", "teamie-werewolf",
    "--project-directory", str(cur / "deploy"),
    "--env-file", str(env_path),
    "--env-file", str(cur / "deploy" / "images.env"),
    "-f", str(cur / "deploy" / "compose.production.yaml"),
    "up", "-d", "--force-recreate", "werewolf",
], check=True)
print("Updated TEAMIE_APP_ID & TEAMIE_APP_SIGNING_SECRET and recreated werewolf container.")
'

python3 -c 'import json,os,sys; json.dump({"app_id":os.environ["TEAMIE_APP_ID"].strip(),"signing_secret":os.environ["TEAMIE_APP_SIGNING_SECRET"].strip()},sys.stdout)' \
  | ssh -o BatchMode=yes "$SSH_TARGET" python3 -c "$REMOTE_SCRIPT"
