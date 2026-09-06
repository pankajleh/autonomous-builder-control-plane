#!/usr/bin/env bash
set -Eeuo pipefail

OWNER="${GITHUB_OWNER:-pankajleh}"
REPO="${GITHUB_REPO:-autonomous-builder-control-plane}"
VISIBILITY="${GITHUB_VISIBILITY:-private}"

command -v gh >/dev/null 2>&1 || {
  echo "ERROR: GitHub CLI (gh) is required." >&2
  exit 1
}

gh auth status >/dev/null

git rev-parse --is-inside-work-tree >/dev/null

if git remote get-url origin >/dev/null 2>&1; then
  echo "origin already exists: $(git remote get-url origin)"
  exit 0
fi

case "$VISIBILITY" in
  private|public|internal) ;;
  *) echo "ERROR: GITHUB_VISIBILITY must be private, public, or internal" >&2; exit 2 ;;
esac

args=(repo create "$OWNER/$REPO" --source=. --remote=origin --push)
case "$VISIBILITY" in
  private) args+=(--private) ;;
  public) args+=(--public) ;;
  internal) args+=(--internal) ;;
esac

echo "Creating GitHub repository $OWNER/$REPO ($VISIBILITY) and pushing main..."
gh "${args[@]}"
