#!/usr/bin/env bash
# Lands the working-tree changes under the given paths on a protected branch
# through a pull request, entirely through GitHub's API.
#
# main's `protect` ruleset only takes changes through a pull request, with
# verified commits. A workflow therefore cannot push there, not even with the
# App's token, and the App is not a bypass actor. This script:
#   1. stops if nothing under the paths changed,
#   2. creates <branch> at the checked-out commit (the base's current head),
#   3. commits the changes to it with api-commit.sh (GitHub signs the commit),
#   4. opens a pull request into <base> and squash-merges it (GitHub signs the
#      squash commit too), then deletes <branch>.
#
# Put "[skip ci]" in the message: it is the head commit of the pull request
# and the title of the squash commit, so neither starts workflows, and a
# workflow that runs on push to <base> does not trigger itself again.
#
# Usage: api-pr.sh <base> <branch> <message> <path>...
# Needs: GH_TOKEN (an App installation token with contents and pull-requests
# write), GITHUB_REPOSITORY, curl, jq. The checkout must be at <base>'s
# current head on the remote.
set -euo pipefail

if [ "$#" -lt 4 ]; then
  echo "usage: $0 <base> <branch> <message> <path>..." >&2
  exit 2
fi
base=$1
branch=$2
message=$3
shift 3
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
api=${GITHUB_API_URL:-https://api.github.com}
here=$(cd "$(dirname "$0")" && pwd)

# -f: generated paths such as docs/code are gitignored for local work.
git add -A -f -- "$@"
if git diff --cached --quiet -- "$@"; then
  echo "Nothing to commit under: $*"
  exit 0
fi

head=$(git rev-parse HEAD)
remote_head=$(git ls-remote --heads origin "refs/heads/${base}" | cut -f1)
if [ "$head" != "$remote_head" ]; then
  echo "::notice::${base} has moved (${remote_head:0:7}, checkout at ${head:0:7}); the run for the newer commit takes over"
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

gh_api() { # method path [json-file]
  local method=$1 path=$2 data=${3:-}
  local args=(-sS -o "$work/response.json" -w '%{http_code}' -X "$method"
    -H "Authorization: Bearer ${GH_TOKEN}"
    -H "Accept: application/vnd.github+json"
    -H "X-GitHub-Api-Version: 2022-11-28")
  [ -n "$data" ] && args+=(--data-binary "@$data")
  curl "${args[@]}" "${api}/repos/${GITHUB_REPOSITORY}${path}"
}

# A branch left over from an earlier failed run would make the create fail.
gh_api DELETE "/git/refs/heads/${branch}" >/dev/null || true

jq -n --arg ref "refs/heads/${branch}" --arg sha "$head" '{ref: $ref, sha: $sha}' >"$work/ref.json"
status=$(gh_api POST /git/refs "$work/ref.json")
if [ "$status" != 201 ]; then
  echo "::error::creating ${branch} failed with HTTP ${status}: $(jq -r '.message // empty' "$work/response.json")" >&2
  exit 1
fi

"$here/api-commit.sh" "$branch" "$message" "$@"
commit=$(git rev-parse HEAD)

headline=$(printf '%s\n' "$message" | head -n 1)
jq -n --arg title "$headline" --arg head "$branch" --arg base "$base" \
  --arg body "Opened by ${GITHUB_WORKFLOW:-a workflow} (run ${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID:-local}). Merged by the same run." \
  '{title: $title, head: $head, base: $base, body: $body}' >"$work/pr.json"
status=$(gh_api POST /pulls "$work/pr.json")
if [ "$status" != 201 ]; then
  echo "::error::opening the pull request failed with HTTP ${status}: $(jq -r '.message // empty' "$work/response.json")" >&2
  exit 1
fi
number=$(jq -r '.number' "$work/response.json")
echo "Opened #${number}: $(jq -r '.html_url' "$work/response.json")"

# GitHub computes mergeability asynchronously right after the PR is opened.
jq -n --arg sha "$commit" --arg title "${headline} (#${number})" \
  '{merge_method: "squash", sha: $sha, commit_title: $title}' >"$work/merge.json"
for attempt in 1 2 3 4 5 6; do
  status=$(gh_api PUT "/pulls/${number}/merge" "$work/merge.json")
  [ "$status" = 200 ] && break
  [ "$status" = 405 ] || [ "$status" = 409 ] || break
  sleep $((attempt * 5))
done
if [ "$status" != 200 ]; then
  echo "::error::merging #${number} failed with HTTP ${status}: $(jq -r '.message // empty' "$work/response.json")" >&2
  exit 1
fi
echo "Merged #${number} into ${base}: $(jq -r '.sha' "$work/response.json")"

gh_api DELETE "/git/refs/heads/${branch}" >/dev/null || true
