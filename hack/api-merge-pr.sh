#!/usr/bin/env bash
# Merges <head> into a protected <base> through a pull request, as a merge
# commit, entirely through GitHub's API, and checks the result out.
#
# main's `protect` ruleset refuses a merge made with the merges API (it is not
# a pull request). This uses the pull request create-release.yml opened for
# the branch, or opens one, and merges it with a merge commit, which GitHub
# makes and signs.
#
# Usage: api-merge-pr.sh <base> <head> <message>
# Needs: GH_TOKEN (an App installation token with contents and pull-requests
# write), GITHUB_REPOSITORY, curl, jq.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <base> <head> <message>" >&2
  exit 2
fi
base=$1
head=$2
message=$3
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
api=${GITHUB_API_URL:-https://api.github.com}
owner=${GITHUB_REPOSITORY%%/*}

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

status=$(gh_api GET "/pulls?state=open&base=${base}&head=${owner}:${head}")
if [ "$status" != 200 ]; then
  echo "::error::listing pull requests failed with HTTP ${status}" >&2
  exit 1
fi
number=$(jq -r '.[0].number // empty' "$work/response.json")

if [ -z "$number" ]; then
  jq -n --arg title "$message" --arg head "$head" --arg base "$base" \
    '{title: $title, head: $head, base: $base,
      body: "Opened by the release workflow to merge \($head) into \($base)."}' >"$work/pr.json"
  status=$(gh_api POST /pulls "$work/pr.json")
  if [ "$status" != 201 ]; then
    echo "::error::opening the pull request failed with HTTP ${status}: $(jq -r '.message // empty' "$work/response.json")" >&2
    exit 1
  fi
  number=$(jq -r '.number' "$work/response.json")
  echo "Opened #${number}"
else
  echo "Using #${number}"
fi

# Merge exactly the head that was checked, and retry while GitHub is still
# computing mergeability for a freshly opened or updated pull request.
sha=$(git ls-remote --heads origin "refs/heads/${head}" | cut -f1)
jq -n --arg sha "$sha" --arg title "$message" \
  '{merge_method: "merge", sha: $sha, commit_title: $title}' >"$work/merge.json"
for attempt in 1 2 3 4 5 6; do
  status=$(gh_api PUT "/pulls/${number}/merge" "$work/merge.json")
  [ "$status" = 200 ] && break
  [ "$status" = 405 ] || [ "$status" = 409 ] || break
  sleep $((attempt * 5))
done
if [ "$status" != 200 ]; then
  echo "::error::merging #${number} (${head} into ${base}) failed with HTTP ${status}: $(jq -r '.message // empty' "$work/response.json")" >&2
  exit 1
fi
merged=$(jq -r '.sha' "$work/response.json")
echo "Merged #${number} (${head} into ${base}): ${merged}"

git fetch --quiet origin "$base"
git checkout --quiet --detach "$merged"
