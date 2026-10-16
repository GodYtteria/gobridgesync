#!/bin/sh
set -eu
tag=${1:?usage: contrib/promote.sh vX.Y.Z | vX.Y.Z-rcN}
prod=${GBS_PROD_REMOTE:-prod}
cd "$(dirname "$0")/.."
case $tag in
*-rc[0-9]*) rcs=$tag ;;
*-*) echo "promote stable versions or release candidates (vX.Y.Z-rcN), not $tag" >&2; exit 1 ;;
*) rcs=$(git tag -l "$tag-rc*") ;;
esac
passed=0
for rc in $rcs; do
	n=$(RC="$rc" gh run list --repo "${GBS_STAGING_REPO:-GodYtteria/gobridgesync-desktop-staging}" --workflow staging.yml --branch "$rc" \
		--json headBranch,conclusion --jq '[.[] | select(.conclusion == "success" and .headBranch == env.RC)] | length')
	passed=$((passed + n))
done
if [ "${passed:-0}" -eq 0 ]; then
	echo "no passing release candidate build: tag $tag-rc1 in staging, wait for it to pass, and test it first" >&2
	exit 1
fi
git rev-parse -q --verify "refs/tags/$tag" >/dev/null || git tag "$tag"
work=$(mktemp -d)
trap 'git worktree remove --force "$work" 2>/dev/null; rm -rf "$work"' EXIT
git fetch -q "$prod" main
git worktree add -q --detach "$work" "$prod/main"
git -C "$work" rm -rqf . >/dev/null
git archive "$tag" | tar -x -C "$work"
git -C "$work" add -A
git -C "$work" commit -qm "Release $tag"
case $tag in
*-*) git -C "$work" push -q "$prod" "HEAD:refs/tags/$tag" ;;
*) git -C "$work" push -q "$prod" HEAD:main "HEAD:refs/tags/$tag" ;;
esac
echo "Promoted $tag to $prod"
