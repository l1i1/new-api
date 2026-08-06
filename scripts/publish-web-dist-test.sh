#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=init.defaultBranch
export GIT_CONFIG_VALUE_0=main

readonly BARE_REPO="$TEST_ROOT/webdist.git"
git init --bare "$BARE_REPO" >/dev/null

for version in \
  v1.0.0-rc.1-tokeness-test.1 \
  v1.0.0-rc.1-tokeness-test.2 \
  v1.0.0-rc.1-tokeness-test.3; do
  artifact_dir="$TEST_ROOT/$version"
  mkdir -p "$artifact_dir"
  printf '<html>%s</html>\n' "$version" > "$artifact_dir/index.html"
  printf 'asset-%s\n' "$version" > "$artifact_dir/main.js"

  ARTIFACT_DIR="$artifact_dir" \
    RELEASE_VERSION="$version" \
    SOURCE_COMMIT="commit-$version" \
    WEBDIST_REPO_URL="file://$BARE_REPO" \
    WEBDIST_REPO_SLUG="l1i1/newapi-webdist" \
    bash scripts/publish-web-dist.sh
done

mapfile -t retained_versions < <(
  git --git-dir "$BARE_REPO" ls-tree -r --name-only web-dist |
    sed -n 's#^releases/\([^/]*\)/.*#\1#p' |
    sort -u
)

[[ "${#retained_versions[@]}" -eq 2 ]] || {
  echo "expected exactly two retained versions, got ${#retained_versions[@]}" >&2
  exit 1
}
[[ "${retained_versions[*]}" == *"v1.0.0-rc.1-tokeness-test.2"* ]] || exit 1
[[ "${retained_versions[*]}" == *"v1.0.0-rc.1-tokeness-test.3"* ]] || exit 1
[[ "${retained_versions[*]}" != *"v1.0.0-rc.1-tokeness-test.1"* ]] || exit 1
git --git-dir "$BARE_REPO" ls-tree -r --name-only web-dist | grep -qx 'index.html'
git --git-dir "$BARE_REPO" ls-tree -r --name-only web-dist | grep -qx 'main.js'

manifest="$(git --git-dir "$BARE_REPO" show web-dist:manifest.json)"
[[ "$(jq -r '.current.version' <<< "$manifest")" == 'v1.0.0-rc.1-tokeness-test.3' ]] || exit 1
[[ "$(jq -r '.previous.version' <<< "$manifest")" == 'v1.0.0-rc.1-tokeness-test.2' ]] || exit 1

echo 'publish-web-dist retention test passed'
