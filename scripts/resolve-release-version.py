#!/usr/bin/env python3
"""Resolve a fork release version and check the embedded version metadata."""

import argparse
import json
import re
from pathlib import Path


def resolve_version(root: Path, ref_type: str, ref_name: str, requested: str) -> str:
    wails = json.loads((root / "wails.json").read_text(encoding="utf-8-sig"))
    package = json.loads((root / "frontend/package.json").read_text(encoding="utf-8-sig"))
    lock = json.loads((root / "frontend/package-lock.json").read_text(encoding="utf-8-sig"))
    versions = {
        "wails.json info.productVersion": wails["info"]["productVersion"],
        "frontend/package.json version": package["version"],
        "frontend/package-lock.json version": lock["version"],
        "frontend/package-lock.json packages[''].version": lock["packages"][""]["version"],
    }
    numeric_version = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    requested = requested.strip()
    if ref_type == "tag":
        match = re.fullmatch(r"czc-v(" + numeric_version + r")", ref_name)
        if not match:
            raise ValueError("Release tags must use czc-vX.Y.Z (for example czc-v1.9.0).")
        version = match.group(1)
        if requested and requested != version:
            raise ValueError("The requested version does not match the release tag.")
    else:
        version = requested or versions["wails.json info.productVersion"]
    if not isinstance(version, str) or not re.fullmatch(numeric_version, version):
        raise ValueError("Package versions must use numeric X.Y.Z.")
    for source, value in versions.items():
        if value != version:
            raise ValueError(f"{source} is {value!r}; expected {version!r}. Update version metadata before building.")
    return version


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref-type", default="branch")
    parser.add_argument("--ref-name", default="master")
    parser.add_argument("--version", default="", help="Expected version; must match the committed metadata.")
    args = parser.parse_args()
    try:
        version = resolve_version(Path(__file__).resolve().parent.parent, args.ref_type, args.ref_name, args.version)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"Release version error: {error}\n")
    print(version)


if __name__ == "__main__":
    main()
