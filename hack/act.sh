#!/usr/bin/env bash
# BlanketOps — local act runner
#
# Usage:
#   ./hack/act.sh                   # run all non-release workflows
#   ./hack/act.sh ci.yaml           # run one specific workflow
#   ./hack/act.sh --dry-run         # validate steps without executing them
#   ./hack/act.sh ci.yaml --dry-run # dry-run a single workflow
#
# Prerequisites:
#   act       — https://nektosact.com
#   docker    — must be running
#
# Secrets file (.secrets in repo root):
#   GH_PAT=ghp_xxxx
#
# SSH key is read from SSH_KEY_PATH (default: ~/.ssh/id_ed25519)
# and passed via --secret, not --secret-file, because it is multiline.

set -euo pipefail

# ── config ────────────────────────────────────────────────────────────────────
SECRETS_FILE="${SECRETS_FILE:-.secrets}"
SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519}"
WORKFLOW_DIR=".github/workflows"

# Workflows to run (release workflows are excluded)
ALL_WORKFLOWS=(
  "apko.yml"
  "ci.yaml"
  "image.yml"
  "ko.yml"
)

# ── args ──────────────────────────────────────────────────────────────────────
DRY_RUN=false
TARGET=""

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    *.yml|*.yaml) TARGET="$arg" ;;
    -h|--help)
      sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown argument: ${arg}"; exit 1 ;;
  esac
done

# ── preflight ─────────────────────────────────────────────────────────────────
fail() { echo "✗ $*" >&2; exit 1; }

command -v act    >/dev/null 2>&1 || fail "act not found — https://nektosact.com"
command -v docker >/dev/null 2>&1 || fail "docker not found"
docker info       >/dev/null 2>&1 || fail "Docker daemon is not running"

[[ -f "$SECRETS_FILE" ]] || fail ".secrets not found. Create it with:
  GH_PAT=ghp_xxxx"

[[ -f "$SSH_KEY_PATH" ]] || fail "SSH key not found at ${SSH_KEY_PATH}
  Override with: SSH_KEY_PATH=/path/to/key ./hack/act.sh"

# ── flags ─────────────────────────────────────────────────────────────────────
SSH_KEY="$(cat "$SSH_KEY_PATH")"

ACT_FLAGS=(
  # Secrets
  --secret-file "$SECRETS_FILE"
  --secret "SSH_PRIVATE_KEY=${SSH_KEY}"

  # Runner image — medium; includes git, go, docker CLI, curl, etc.
  --platform "ubuntu-latest=catthehacker/ubuntu:act-latest"

  # Don't re-pull the runner image on every run
  --pull=false

  # Mount the host Docker socket so ko / docker build work inside the runner
  --container-daemon-socket /var/run/docker.sock

  # workflow_dispatch input defaults
  --input "sign_and_attest=true"
)

$DRY_RUN && ACT_FLAGS+=(--dryrun)

# ── runner ────────────────────────────────────────────────────────────────────
PASSED=()
FAILED=()
SKIPPED=()

run_workflow() {
  local wf="$1"
  local path="${WORKFLOW_DIR}/${wf}"
  local label="${wf}$($DRY_RUN && echo ' [dry-run]' || true)"

  if [[ ! -f "$path" ]]; then
    echo "  ⚠  ${wf} — not found in ${WORKFLOW_DIR}/, skipping"
    SKIPPED+=("$wf")
    return 0
  fi

  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "▶  ${label}"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  if act workflow_dispatch \
       --workflows "$path" \
       "${ACT_FLAGS[@]}"; then
    PASSED+=("$wf")
  else
    FAILED+=("$wf")
  fi
}

# ── single-workflow mode ──────────────────────────────────────────────────────
if [[ -n "$TARGET" ]]; then
  run_workflow "$TARGET"
  echo ""
  [[ ${#FAILED[@]} -eq 0 ]] && echo "✅ ${TARGET} passed" || echo "❌ ${TARGET} failed"
  exit ${#FAILED[@]}
fi

# ── run all ───────────────────────────────────────────────────────────────────
echo "▶  BlanketOps act suite — $(date '+%Y-%m-%d %H:%M:%S')"
echo "   dry-run : ${DRY_RUN}"
echo "   secrets : ${SECRETS_FILE}"
echo "   ssh key : ${SSH_KEY_PATH}"

for wf in "${ALL_WORKFLOWS[@]}"; do
  run_workflow "$wf"
done

# ── summary ───────────────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "Results"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
for wf in "${PASSED[@]+"${PASSED[@]}"}";  do echo "  ✅ ${wf}"; done
for wf in "${FAILED[@]+"${FAILED[@]}"}";  do echo "  ❌ ${wf}"; done
for wf in "${SKIPPED[@]+"${SKIPPED[@]}"}"; do echo "  ⚠  ${wf} (skipped — file not found)"; done

[[ ${#FAILED[@]} -eq 0 ]]