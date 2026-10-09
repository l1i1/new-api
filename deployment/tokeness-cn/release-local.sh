#!/usr/bin/env bash
# release-local.sh - build the mainland release image locally and print (or run)
# the rollout, without the CNB pipeline.
#
# Why this exists (2026-10-10): CNB ran out of build quota and every build after
# 16:57 UTC died in the `Prepare` stage, so a tag push no longer produces an
# image. The deploy half never needed CNB - deploy.sh talks to Aliyun and the
# hosts directly - so the release path is: build here, push to the same
# registry, then hand deploy.sh the digest.
#
# Usage (build and push, then copy the printed deploy command):
#
#   bash tools/secrets-broker/broker.sh exec --cred cnb -- \
#     bash deployment/tokeness-cn/release-local.sh v1.0.0-rc.40-tokeness-mainland.45
#
#   # after that, with a session that holds the deploy credentials:
#   bash deployment/tokeness-cn/deploy.sh deploy-release <tag> sha256:<digest>
#
# Flags:
#   --tag <tag>        release tag (or pass it positionally)
#   --cache-from <img> cache source image (default: the newest local ml-<tag>,
#                      else ml-latest from the registry)
#   --no-cache         skip the cache source (full rebuild; slow - the configured
#                      public mirrors crawl at ~0.2 MB/s, while our own registry
#                      pulled the previous release in 10s)
#   --build-only       build, do not push
#   --deploy           also run deploy.sh deploy-release (needs the full set of
#                      deploy credentials in this environment; normally you run
#                      that step separately)
#
# VERSION is written to the tag name, exactly as CNB's tag_push line does, and
# the Go binary bakes it in through -ldflags. deploy.sh verifies it, so the image
# must be built from the commit the tag points at.
set -Eeuo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REGISTRY="docker.cnb.cool"
IMAGE_NAME="${IMAGE_NAME:-$REGISTRY/imvhb/new-api-cn}"
DOCKERFILE="${DOCKERFILE:-Dockerfile.tokeness}"
SITE_FLAVOR="${SITE_FLAVOR:-mainland}"
ICP_BEIAN="${VITE_ICP_BEIAN:-蜀ICP备2026014369号-5}"
POLICE_BEIAN="${VITE_POLICE_BEIAN:-川公网安备51052202010119号}"

TAG=""; CACHE_FROM=""; USE_CACHE=1; BUILD_ONLY=0; DO_DEPLOY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --cache-from) CACHE_FROM="$2"; shift 2 ;;
    --no-cache) USE_CACHE=0; shift ;;
    --build-only) BUILD_ONLY=1; shift ;;
    --deploy) DO_DEPLOY=1; shift ;;
    -h|--help) sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "release-local: unknown flag $1" >&2; exit 2 ;;
    *) TAG="$1"; shift ;;
  esac
done

log() { printf '[release-local] %s\n' "$*"; }
die() { printf '[release-local] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -n "$TAG" ]] || die "a release tag is required (see --help)"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+.*-tokeness-mainland\.[0-9]+$ ]] \
  || die "tag '$TAG' does not look like a mainland release tag"
cd "$REPO_DIR"
[[ -f "$DOCKERFILE" ]] || die "$DOCKERFILE not found in $REPO_DIR"

# The image must come from the commit the tag points at: deploy.sh compares the
# version the container reports against the tag, and a mismatched image would be
# rejected after the master had already been rolled.
tag_commit="$(git rev-parse --verify --quiet "$TAG^{commit}")" || die "tag $TAG does not exist locally (push it first)"
head_commit="$(git rev-parse HEAD)"
if [[ "$tag_commit" != "$head_commit" ]]; then
  die "HEAD ($head_commit) is not $TAG ($tag_commit); check the tag out or rebuild it"
fi
# Build from the committed tree or not at all: the image bakes VERSION from the
# tag, so uncommitted source would ship under a version that does not describe it,
# and deploy.sh's version check cannot tell the difference. CI gets this for free
# by building a fresh clone.
dirty="$(git status --porcelain -- . | grep -v '^?? VERSION$' | grep -v '^ M VERSION$' || true)"
[[ -z "$dirty" ]] || die "working tree has uncommitted changes:$(
printf '\n%s' "$dirty")"

log "building $IMAGE_NAME:ml-$TAG from $tag_commit"
printf '%s\n' "$TAG" > VERSION
log "VERSION=$TAG (what CNB's tag_push line writes)"

docker info >/dev/null 2>&1 || die "the docker daemon is not reachable"

if (( USE_CACHE )); then
  if [[ -z "$CACHE_FROM" ]]; then
    # Newest local ml-* image first: pulling the previous release from our own
    # registry takes seconds, while the daemon's configured public mirrors crawl.
    CACHE_FROM="$(docker images --format '{{.Repository}}:{{.Tag}}' "$IMAGE_NAME" 2>/dev/null | grep ':ml-' | head -1 || true)"
    [[ -n "$CACHE_FROM" ]] || CACHE_FROM="$IMAGE_NAME:ml-latest"
  fi
  if docker pull "$CACHE_FROM" >/dev/null 2>&1; then
    log "cache source: $CACHE_FROM"
  else
    log "WARNING: could not pull $CACHE_FROM; building without a cache source (slow)"
    CACHE_FROM=""
  fi
fi

build_args=(--build-arg "VITE_SITE_FLAVOR=$SITE_FLAVOR"
            --build-arg "VITE_ICP_BEIAN=$ICP_BEIAN"
            --build-arg "VITE_POLICE_BEIAN=$POLICE_BEIAN")
cache_args=()
[[ -n "$CACHE_FROM" ]] && cache_args=(--cache-from "$CACHE_FROM")

log "docker build (this is the CPU-heavy step; Go and bun use every core)"
docker build "${cache_args[@]}" "${build_args[@]}" \
  -t "$IMAGE_NAME:ml-$TAG" -f "$DOCKERFILE" . \
  || die "build failed"

if (( BUILD_ONLY )); then
  log "built $IMAGE_NAME:ml-$TAG (not pushed)"
  exit 0
fi

# Release images are immutable: the tag names a version and deploy.sh verifies the
# running container reports it, so re-pushing one silently replaces a shipped
# artifact. Refuse unless the operator says so explicitly.
if docker buildx imagetools inspect "$IMAGE_NAME:ml-$TAG" >/dev/null 2>&1; then
  if [[ "${ALLOW_TAG_OVERWRITE:-0}" == "1" ]]; then
    log "WARNING: $IMAGE_NAME:ml-$TAG already exists in the registry; overwriting because ALLOW_TAG_OVERWRITE=1"
  else
    die "$IMAGE_NAME:ml-$TAG already exists in the registry; release tags are immutable (set ALLOW_TAG_OVERWRITE=1 only to repair one)"
  fi
fi

log "pushing $IMAGE_NAME:ml-$TAG"
docker push "$IMAGE_NAME:ml-$TAG" || die "push failed (is the registry credential in this environment?)"

digest="$(docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:ml-$TAG" 2>/dev/null | cut -d@ -f2)"
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "could not read the pushed digest for $IMAGE_NAME:ml-$TAG"
log "digest: $digest"

if (( DO_DEPLOY )); then
  log "rolling out with deploy.sh (this takes hours: master, then batch rounds with drain windows)"
  exec bash "$REPO_DIR/deployment/tokeness-cn/deploy.sh" deploy-release "$TAG" "$digest"
fi

cat <<EOF

Built and pushed. Roll it out with:

  bash deployment/tokeness-cn/deploy.sh deploy-release $TAG $digest

That command needs the deploy credentials (aliyun, host ssh, oss) in its
environment - it does not use the registry credential this script needed.
EOF
