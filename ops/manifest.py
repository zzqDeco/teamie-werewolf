#!/usr/bin/env python3
"""Immutable release manifest tool for teamie-werewolf (aligned with zzqDeco/teamie)."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import re
import sys

SEMVER_RE = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SHA40_RE = re.compile(r"^[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
HOST_RE = re.compile(r"^[a-z0-9][a-z0-9.-]*[a-z0-9]$")
NS_RE = re.compile(r"^[a-z0-9][a-z0-9._-]*$")
REQUIRED_SERVICES = ("werewolf", "caddy")


def validate_acr(public_host: str, pull_host: str, namespace: str) -> bool:
    """Validate ACR variables if configured; returns True when ACR mode is active."""
    vals = [public_host.strip(), pull_host.strip(), namespace.strip()]
    if not any(vals):
        return False
    if not all(vals):
        raise SystemExit(
            "Partial ACR configuration: ACR_REGISTRY_PUBLIC_HOST, ACR_REGISTRY_PULL_HOST, "
            "and ACR_NAMESPACE must either all be set or all be empty."
        )
    if not HOST_RE.fullmatch(vals[0]) or not HOST_RE.fullmatch(vals[1]):
        raise SystemExit("Invalid ACR registry hostname.")
    if not NS_RE.fullmatch(vals[2]):
        raise SystemExit("Invalid ACR namespace.")
    return True


def cmd_registry_check(args: argparse.Namespace) -> None:
    acr_active = validate_acr(
        args.registry_public_host or "",
        args.registry_pull_host or "",
        args.registry_namespace or "",
    )
    mode = "acr" if acr_active else "ghcr-bundle"
    print(json.dumps({"ok": True, "mode": mode}))


def cmd_create(args: argparse.Namespace) -> None:
    if not SEMVER_RE.fullmatch(args.version):
        raise SystemExit(f"Invalid semantic version: {args.version}")
    if not SHA40_RE.fullmatch(args.sha):
        raise SystemExit(f"Invalid 40-char commit sha: {args.sha}")
    if args.main_sha != args.sha:
        raise SystemExit("Release commit SHA must match main SHA.")

    acr_active = validate_acr(
        args.registry_public_host or "",
        args.registry_pull_host or "",
        args.registry_namespace or "",
    )

    digests_dir = pathlib.Path(args.digests)
    images: dict[str, str] = {}
    for svc in REQUIRED_SERVICES:
        p = digests_dir / f"{svc}.json"
        if not p.is_file():
            raise SystemExit(f"Missing image metadata file: {p}")
        item = json.loads(p.read_text(encoding="utf-8"))
        if item.get("service") != svc:
            raise SystemExit(f"Mismatched service name in {p}")
        image_ref = str(item.get("image", "")).strip()
        if "@" in image_ref:
            _, digest = image_ref.rsplit("@", 1)
            if not DIGEST_RE.fullmatch(digest):
                raise SystemExit(f"Invalid image digest in {image_ref}")
        elif not image_ref:
            raise SystemExit(f"Empty image reference for {svc}")
        images[svc] = image_ref

    manifest = {
        "schemaVersion": 1,
        "repository": args.repository,
        "version": args.version,
        "sha": args.sha,
        "deliveryMode": "acr" if acr_active else "bundle",
        "createdAt": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "images": images,
    }
    json.dump(manifest, sys.stdout, indent=2)
    sys.stdout.write("\n")


def cmd_verify(args: argparse.Namespace) -> None:
    p = pathlib.Path(args.manifest)
    data = json.loads(p.read_text(encoding="utf-8"))
    if data.get("schemaVersion") != 1:
        raise SystemExit("Unsupported manifest schemaVersion.")
    if not SEMVER_RE.fullmatch(str(data.get("version", ""))):
        raise SystemExit("Invalid manifest version.")
    if not SHA40_RE.fullmatch(str(data.get("sha", ""))):
        raise SystemExit("Invalid manifest sha.")
    images = data.get("images") or {}
    for svc in REQUIRED_SERVICES:
        if svc not in images or not str(images[svc]).strip():
            raise SystemExit(f"Missing required image '{svc}' in manifest.")
    if args.print_env:
        print(f"RELEASE_VERSION={data['version']}")
        print(f"RELEASE_SHA={data['sha']}")
        print(f"DELIVERY_MODE={data.get('deliveryMode', 'bundle')}")
        print(f"WEREWOLF_IMAGE={images['werewolf']}")
        print(f"CADDY_IMAGE={images['caddy']}")


def main() -> None:
    parser = argparse.ArgumentParser(description="Release manifest utility")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_reg = sub.add_parser("registry-check")
    p_reg.add_argument("--registry-public-host", default="")
    p_reg.add_argument("--registry-pull-host", default="")
    p_reg.add_argument("--registry-namespace", default="")
    p_reg.set_defaults(func=cmd_registry_check)

    p_create = sub.add_parser("create")
    p_create.add_argument("--repository", required=True)
    p_create.add_argument("--version", required=True)
    p_create.add_argument("--sha", required=True)
    p_create.add_argument("--main-sha", required=True)
    p_create.add_argument("--digests", required=True)
    p_create.add_argument("--registry-public-host", default="")
    p_create.add_argument("--registry-pull-host", default="")
    p_create.add_argument("--registry-namespace", default="")
    p_create.set_defaults(func=cmd_create)

    p_verify = sub.add_parser("verify")
    p_verify.add_argument("--manifest", required=True)
    p_verify.add_argument("--print-env", action="store_true")
    p_verify.set_defaults(func=cmd_verify)

    parsed = parser.parse_args()
    parsed.func(parsed)


if __name__ == "__main__":
    main()
