#!/usr/bin/env bash
set -Eeuo pipefail

: "${ARTIFACT_DIR:?ARTIFACT_DIR is required}"
: "${RELEASE_VERSION:?RELEASE_VERSION is required}"
: "${WEBDIST_REPO_URL:?WEBDIST_REPO_URL is required}"
: "${SOURCE_COMMIT:?SOURCE_COMMIT is required}"

readonly TARGET_BRANCH="${TARGET_BRANCH:-web-dist}"
readonly REPO_SLUG="${WEBDIST_REPO_SLUG:-l1i1/newapi-webdist}"
readonly WORK_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

[[ -f "$ARTIFACT_DIR/index.html" ]] || {
  echo "ERROR: frontend artifact is missing index.html" >&2
  exit 1
}
[[ "$RELEASE_VERSION" =~ ^v[0-9A-Za-z._-]+-tokeness-[0-9A-Za-z._-]+$ ]] || {
  echo "ERROR: release version is not a valid immutable Tokeness version" >&2
  exit 1
}

remote_url="$WEBDIST_REPO_URL"
if [[ "$remote_url" == https://github.com/* && -n "${WEBDIST_TOKEN:-}" ]]; then
  remote_url="https://x-access-token:${WEBDIST_TOKEN}@github.com/${REPO_SLUG}.git"
fi

export GIT_TERMINAL_PROMPT=0

if git ls-remote --exit-code --heads "$remote_url" "$TARGET_BRANCH" >/dev/null 2>&1; then
  git clone --depth 1 --branch "$TARGET_BRANCH" "$remote_url" "$WORK_DIR"
else
  git init "$WORK_DIR" >/dev/null
  git -C "$WORK_DIR" checkout --orphan "$TARGET_BRANCH" >/dev/null
  git -C "$WORK_DIR" remote add origin "$remote_url"
fi
git -C "$WORK_DIR" config user.name "github-actions[bot]"
git -C "$WORK_DIR" config user.email "github-actions[bot]@users.noreply.github.com"

current_version=""
previous_version=""
if [[ -f "$WORK_DIR/manifest.json" ]]; then
  current_version="$(jq -er '.current.version // empty' "$WORK_DIR/manifest.json" 2>/dev/null || true)"
  previous_version="$(jq -er '.previous.version // empty' "$WORK_DIR/manifest.json" 2>/dev/null || true)"
fi

mkdir -p "$WORK_DIR/releases"
rm -rf "$WORK_DIR/releases/$RELEASE_VERSION"
mkdir -p "$WORK_DIR/releases/$RELEASE_VERSION"
cp -a "$ARTIFACT_DIR/." "$WORK_DIR/releases/$RELEASE_VERSION/"

keep_versions=("$RELEASE_VERSION")
if [[ -n "$current_version" && "$current_version" != "$RELEASE_VERSION" ]]; then
  keep_versions+=("$current_version")
elif [[ -n "$previous_version" && "$previous_version" != "$RELEASE_VERSION" ]]; then
  keep_versions+=("$previous_version")
fi

while IFS= read -r release_dir; do
  release_name="$(basename "$release_dir")"
  keep=false
  for version in "${keep_versions[@]}"; do
    [[ "$release_name" == "$version" ]] && keep=true
  done
  [[ "$keep" == true ]] || rm -rf "$release_dir"
done < <(find "$WORK_DIR/releases" -mindepth 1 -maxdepth 1 -type d -print)

manifest_previous_version=""
if [[ "${#keep_versions[@]}" -gt 1 ]]; then
  manifest_previous_version="${keep_versions[1]}"
fi

# Keep root paths compatible with the existing CDN URL. Hashed assets from
# the previous snapshot remain available while current HTML and assets win.
find "$WORK_DIR" -mindepth 1 -maxdepth 1 \
  ! -name .git ! -name releases ! -name manifest.json ! -name .deployed-version \
  -exec rm -rf {} +
if [[ -n "$manifest_previous_version" ]]; then
  cp -a "$WORK_DIR/releases/$manifest_previous_version/." "$WORK_DIR/"
fi
cp -a "$WORK_DIR/releases/$RELEASE_VERSION/." "$WORK_DIR/"

jq -n \
  --arg current "$RELEASE_VERSION" \
  --arg previous "$manifest_previous_version" \
  --arg source_commit "$SOURCE_COMMIT" \
  --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{
    schema_version: 1,
    current: {version: $current, path: ("releases/" + $current)},
    previous: (if $previous == "" then null else {version: $previous, path: ("releases/" + $previous)} end),
    source_commit: $source_commit,
    generated_at: $generated_at
  }' > "$WORK_DIR/manifest.json"

printf '%s\n' "$RELEASE_VERSION" > "$WORK_DIR/.deployed-version"

git -C "$WORK_DIR" add -A
if git -C "$WORK_DIR" diff --cached --quiet; then
  echo "Frontend distribution is already synchronized: $RELEASE_VERSION"
  exit 0
fi

git -C "$WORK_DIR" commit -m "deploy: $RELEASE_VERSION [skip ci]" >/dev/null
git -C "$WORK_DIR" push origin "HEAD:$TARGET_BRANCH"

if [[ -n "$manifest_previous_version" ]]; then
  echo "Published $RELEASE_VERSION and retained previous $manifest_previous_version"
else
  echo "Published initial frontend distribution $RELEASE_VERSION"
fi
