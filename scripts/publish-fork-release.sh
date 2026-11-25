#!/usr/bin/env bash
# Publish assets only for the exact fork tag that triggered this run.
set -euo pipefail

if [[ ${GITHUB_EVENT_NAME:-} != push || ${GITHUB_REF_TYPE:-} != tag ]]; then
  echo "Only pushes of a fork release tag can publish assets." >&2
  exit 1
fi
tag=${GITHUB_REF_NAME:?}
python3 scripts/resolve-release-version.py --ref-type tag --ref-name "$tag"
: "${GH_REPO:?Missing release repository}"
if ! git merge-base --is-ancestor HEAD refs/remotes/origin/master; then
  echo "The tagged commit is outside origin/master." >&2
  exit 1
fi

draft=$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || true)
if [[ $draft == false ]]; then
  echo "Release $tag is already published."
  exit 0
fi

files=()
for file in release-artifacts/*; do
  [[ -f "$file" ]] && files+=("$file")
done
if (( ${#files[@]} == 0 )); then
  echo "No release assets found." >&2
  exit 1
fi

if [[ $draft != true ]]; then
  notes_file=$(mktemp)
  trap 'rm -f "$notes_file"' EXIT
  notes_args=(--generate-notes)
  if [[ -f "docs/changelogs/$tag.md" ]]; then
    cat "docs/changelogs/$tag.md" > "$notes_file"
    notes_args=()
  fi
  if [[ -f VERSION ]]; then
    full_version=$(<VERSION)
    printf '\n本地版本 `%s`\n' "${full_version%$'\r'}" >> "$notes_file"
  fi
  baseline=$(git log -1 --first-parent --merges --grep='^Upstream-Commit: ' \
    --format='%(trailers:key=Upstream-Ref,key=Upstream-Commit)')
  if [[ -n "$baseline" ]]; then
    printf '\n%s\n' "$baseline" >> "$notes_file"
  fi
  if [[ -s "$notes_file" ]]; then
    notes_args+=(--notes-file "$notes_file")
  fi
  gh release create "$tag" --verify-tag --draft --title "$tag" "${notes_args[@]}"
fi

gh release upload "$tag" "${files[@]}" --clobber
gh release edit "$tag" --draft=false
