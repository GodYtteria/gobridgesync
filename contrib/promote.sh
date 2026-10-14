#!/bin/sh
set -eu
tag=${1:?usage: contrib/promote.sh vX.Y.Z}
prod=${GBS_PROD_REMOTE:-prod}
cd "$(dirname "$0")/.."
git rev-parse -q --verify "refs/tags/$tag" >/dev/null || git tag "$tag"
work=$(mktemp -d)
trap 'git worktree remove --force "$work" 2>/dev/null; rm -rf "$work"' EXIT
git fetch -q "$prod" main
git worktree add -q --detach "$work" "$prod/main"
git -C "$work" rm -rqf . >/dev/null
git archive "$tag" | tar -x -C "$work"
git -C "$work" add -A
git -C "$work" commit -qm "Release $tag"
git -C "$work" push -q "$prod" HEAD:main "HEAD:refs/tags/$tag"
echo "Promoted $tag to $prod"
