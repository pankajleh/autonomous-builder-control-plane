#!/usr/bin/env python3
"""Compare the live local-integration ABCP operator configuration with its versioned copy.

Versioned files are compared as parsed JSON, so formatting differences are not drift. Secret files are never
read: only their presence and owner-only permissions are checked. Exit status 1 reports drift.
"""
import argparse, json, stat, sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
VERSIONED = REPO / "deploy/local-integration/abcp-config"
SECRETS = ("token", "cursor.json", "preview-session-signing.hex", "preview-gateway-service.token", "workflow.json")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--live", default="/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config")
    live = Path(parser.parse_args().live)
    drift = False
    for versioned in sorted(VERSIONED.glob("*.json")):
        current = live / versioned.name
        if not current.is_file():
            print(f"MISSING  {versioned.name}"); drift = True; continue
        same = json.loads(versioned.read_text()) == json.loads(current.read_text())
        print(f"{'OK      ' if same else 'DRIFT   '} {versioned.name}")
        drift |= not same
    for name in SECRETS:
        path = live / name
        if not path.is_file():
            print(f"MISSING  {name} (secret, not versioned)"); drift = True; continue
        mode = stat.S_IMODE(path.stat().st_mode)
        print(f"{'OK      ' if mode & 0o077 == 0 else 'UNSAFE  '} {name} (secret, not versioned, mode {mode:o})")
        drift |= mode & 0o077 != 0
    return 1 if drift else 0


if __name__ == "__main__":
    sys.exit(main())
