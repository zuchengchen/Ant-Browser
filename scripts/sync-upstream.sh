#!/usr/bin/env bash

set -euo pipefail

SYNC_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC_REF="refs/heads/master"
SYNC_CHECK_ONLY=false

usage() {
  cat <<'EOF'
Usage: bash scripts/sync-upstream.sh [--check] [--ref <upstream-ref>]

  --check       Fetch and show incoming commits without changing the worktree.
  --ref <ref>   Select refs/heads/<branch>, refs/tags/<tag>, or a full commit SHA.
                Default: refs/heads/master. Tags are fetched into FETCH_HEAD only.
  --help        Show this help.

Run on a clean master branch to prepare a merge. The script does not commit,
push, create a PR, or publish a release. Review and test before committing.
EOF
}

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check)
      SYNC_CHECK_ONLY=true
      shift
      ;;
    --ref)
      [[ $# -ge 2 && -n "$2" ]] || fail "--ref requires an upstream ref."
      SYNC_REF="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "Unknown argument: $1. Use --help for usage."
      ;;
  esac
done

cd "$SYNC_ROOT"
git rev-parse --show-toplevel >/dev/null
git remote get-url origin >/dev/null || fail "Configure origin for your fork first."
git remote get-url upstream >/dev/null || fail "Configure upstream first; see docs/upstream-sync.md."

case "$SYNC_REF" in
  refs/heads/*|refs/tags/*)
    git check-ref-format "$SYNC_REF" || fail "Invalid upstream ref: $SYNC_REF"
    ;;
  *)
    [[ "$SYNC_REF" =~ ^[0-9a-fA-F]{40}$ ]] || fail "Use a full refs/heads/..., refs/tags/..., or 40-character commit SHA."
    ;;
esac

for state in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD rebase-merge rebase-apply; do
  [[ ! -e "$(git rev-parse --git-path "$state")" ]] || fail "Finish or abort the existing Git operation first ($state)."
done

if ! "$SYNC_CHECK_ONLY"; then
  [[ "$(git symbolic-ref --quiet --short HEAD || true)" == master ]] || fail "Switch to master before merging upstream."
  [[ -z "$(git status --porcelain --untracked-files=normal)" ]] || fail "The worktree is dirty. Commit or otherwise preserve your changes before syncing."
fi

# Explicit refspecs prevent copying upstream tags into our release namespace.
git fetch --no-tags origin refs/heads/master
if ! "$SYNC_CHECK_ONLY" && ! git merge-base --is-ancestor origin/master HEAD; then
  fail "Local master does not contain origin/master. Reconcile it first (git pull --ff-only origin master if only behind)."
fi
git fetch --no-tags upstream "$SYNC_REF"
SYNC_SHA="$(git rev-parse --verify 'FETCH_HEAD^{commit}')"
SYNC_REPOSITORY="$(git remote get-url upstream)"

printf 'Upstream ref: %s\nUpstream commit: %s\n' "$SYNC_REF" "$SYNC_SHA"
git merge-base HEAD "$SYNC_SHA" >/dev/null || fail "No common ancestor. Investigate the upstream history before merging."

if git merge-base --is-ancestor "$SYNC_SHA" HEAD; then
  printf 'Already integrated: no upstream commits to merge.\n'
  exit 0
fi

printf '\nIncoming commits (up to 30):\n'
git --no-pager log --oneline --max-count=30 "HEAD..$SYNC_SHA"
printf '\nUpstream changes since the common ancestor:\n'
git --no-pager diff --stat "HEAD...$SYNC_SHA"

if "$SYNC_CHECK_ONLY"; then
  printf '\nCheck complete. The worktree and local branch were not changed.\n'
  exit 0
fi

SYNC_MESSAGE="chore: sync upstream $SYNC_REF

Upstream-Repository: $SYNC_REPOSITORY
Upstream-Ref: $SYNC_REF
Upstream-Commit: $SYNC_SHA"

if ! git merge --no-ff --no-commit -m "$SYNC_MESSAGE" "$SYNC_SHA"; then
  printf '\nMerge stopped. Inspect git status and resolve conflicts without discarding fork changes.\n' >&2
  printf 'After resolving, stage the affected files, run validation, then git commit --no-edit.\n' >&2
  printf 'To cancel this pending merge: git merge --abort\n' >&2
  exit 1
fi

printf '\nMerge prepared; HEAD is unchanged and the merge has not been committed.\n'
printf 'Review git diff --cached and run the checks in docs/upstream-sync.md.\n'
printf 'After validation: git commit --no-edit\n'
printf 'Then push your branch: git push origin master\n'
printf 'To cancel this pending merge: git merge --abort\n'
