#!/usr/bin/env bash
# BlanketOps — local ko build
#
# Usage:
#   ./scripts/local-ko.sh          # build + push to GHCR (mirrors CI exactly)
#   ./scripts/local-ko.sh kind     # build + load into local kind cluster (no push, no sign)
#
# Required env vars:
#   GH_PAT   — personal access token with write:packages scope  (push mode only)
#
# Optional env vars:
#   REF_NAME — override the tag/branch name (defaults to current git tag or branch)

set -euo pipefail

# ── config ────────────────────────────────────────────────────────────────────
REPO_OWNER="ntlaletsi70"
REPO="${REPO_OWNER}/blanketops-environments-controller"
REF_NAME="${REF_NAME:-$(git describe --tags --exact-match 2>/dev/null || git rev-parse --abbrev-ref HEAD)}"
GIT_SHA="$(git rev-parse HEAD)"
MODE="${1:-push}"

echo "▶  BlanketOps Environments Controller local ko build"
echo "   mode : ${MODE}"
echo "   ref  : ${REF_NAME}"
echo "   sha  : ${GIT_SHA:0:12}"
echo ""

# ── shared: regenerate CRDs from markers ─────────────────────────────────────
mage manifests

# ── mode: push (mirrors CI) ───────────────────────────────────────────────────
if [[ "$MODE" == "push" ]]; then
  : "${GH_PAT:?GH_PAT must be set for push mode — export GH_PAT=<token>}"

  export KO_DOCKER_REPO="ghcr.io/${REPO}/controller"
  ARTIFACT_REF="ghcr.io/${REPO_OWNER}/environments-controller:${REF_NAME}"

  # Login — ko and oras have separate credential stores
  echo "$GH_PAT" | ko login ghcr.io \
    --username "$REPO_OWNER" \
    --password-stdin
  echo "$GH_PAT" | oras login ghcr.io \
    --username "$REPO_OWNER" \
    --password-stdin

  # Build + push multi-arch image
  ko build ./cmd --bare \
    --platform linux/amd64,linux/arm64 \
    --tags "${REF_NAME},latest" \
    --image-refs /tmp/ko-image-refs

  DIGEST="$(tail -1 /tmp/ko-image-refs | cut -d@ -f2)"

  # Sign
  cosign sign --yes "${KO_DOCKER_REPO}@${DIGEST}"

  # Publish CRD artifact via ORAS
  FILE_ARGS=()
  for f in config/crd/bases/*.yaml; do
    [[ -f "$f" ]] || { echo "✗ No CRD manifests found in config/crd/bases/ — run mage manifests first"; exit 1; }
    FILE_ARGS+=("${f}:application/vnd.blanketops.crd.v1+yaml")
  done

  oras push "$ARTIFACT_REF" \
    --annotation "org.opencontainers.image.source=https://github.com/${REPO}" \
    --annotation "org.opencontainers.image.revision=${GIT_SHA}" \
    "${FILE_ARGS[@]}"

  echo ""
  echo "✅ Image:    ${KO_DOCKER_REPO}@${DIGEST}"
  echo "✅ Artifact: ${ARTIFACT_REF}"

# ── mode: kind (local cluster, no push, no sign) ─────────────────────────────
elif [[ "$MODE" == "kind" ]]; then
  # ko.local builds to the local Docker daemon — no registry needed
  export KO_DOCKER_REPO="ko.local"

  ko build ./cmd --bare \
    --tags "${REF_NAME}" \
    --image-refs /tmp/ko-image-refs

  IMAGE="$(tail -1 /tmp/ko-image-refs)"

  kind load docker-image "$IMAGE"

  echo ""
  echo "✅ Loaded into kind: ${IMAGE}"
  echo ""
  echo "   Patch your controller deployment:"
  echo "   kubectl set image deployment/blanketops-environments-controller \\"
  echo "     manager=${IMAGE} -n blanketops-system"

else
  echo "Usage: $0 [push|kind]"
  echo ""
  echo "  push  — build linux/amd64+arm64, push to GHCR, sign with cosign, publish CRD artifact"
  echo "  kind  — build native arch only, load into local kind cluster (no registry, no sign)"
  exit 1
fi