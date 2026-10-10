#!/usr/bin/env bash
# Commits the working-tree changes under the given paths to a branch through
# GitHub's API, without `git commit` or `git push`.
#
# A commit made through the API with an App installation token is signed by
# GitHub and shows as verified. A commit made with git in a workflow is not,
# whatever key signs it, and a branch that requires verified signatures
# refuses it. The release workflows use this for every commit they add.
#
# Usage: api-commit.sh <branch> <message> <path>...
# Needs: GH_TOKEN, GITHUB_REPOSITORY, curl, jq. The checkout must be at the
# branch's current head on the remote.
set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <branch> <message> <path>..." >&2
  exit 2
fi
branch=$1
message=$2
shift 2
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
api=${GITHUB_GRAPHQL_URL:-https://api.github.com/graphql}

# -f: docs/code is listed in .gitignore for local work and committed by CI.
git add -A -f -- "$@"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git diff --cached --name-only -z --diff-filter=ACMRT -- "$@" >"$work/changed"
git diff --cached --name-only -z --diff-filter=D -- "$@" >"$work/deleted"
if [ ! -s "$work/changed" ] && [ ! -s "$work/deleted" ]; then
  echo "Nothing to commit under: $*"
  exit 0
fi

: >"$work/additions.jsonl"
while IFS= read -r -d '' path; do
  base64 -w0 "$path" >"$work/blob"
  jq -n --arg path "$path" --rawfile contents "$work/blob" \
    '{path: $path, contents: $contents}' >>"$work/additions.jsonl"
done <"$work/changed"

: >"$work/deletions.jsonl"
while IFS= read -r -d '' path; do
  jq -n --arg path "$path" '{path: $path}' >>"$work/deletions.jsonl"
done <"$work/deleted"

headline=$(printf '%s\n' "$message" | head -n 1)
body=$(printf '%s\n' "$message" | tail -n +3)

# The API refuses the commit unless the branch is still where we think it is.
expected=$(git rev-parse HEAD)

# shellcheck disable=SC2016 # $input is a GraphQL variable, not a shell one.
query='mutation($input: CreateCommitOnBranchInput!) { createCommitOnBranch(input: $input) { commit { oid url } } }'

jq -n \
  --arg query "$query" \
  --arg repo "$GITHUB_REPOSITORY" \
  --arg branch "$branch" \
  --arg headline "$headline" \
  --arg body "$body" \
  --arg expected "$expected" \
  --slurpfile additions "$work/additions.jsonl" \
  --slurpfile deletions "$work/deletions.jsonl" \
  '{query: $query, variables: {input: {
      branch: {repositoryNameWithOwner: $repo, branchName: $branch},
      message: {headline: $headline, body: $body},
      expectedHeadOid: $expected,
      fileChanges: {additions: $additions, deletions: $deletions}
  }}}' >"$work/request.json"

curl -sS --fail-with-body \
  -H "Authorization: bearer ${GH_TOKEN}" \
  -H "Content-Type: application/json" \
  --data-binary "@$work/request.json" \
  "$api" >"$work/response.json"

if jq -e '.errors' "$work/response.json" >/dev/null; then
  echo "::error::commit to ${branch} was refused" >&2
  jq -r '.errors[].message' "$work/response.json" >&2
  exit 1
fi

oid=$(jq -r '.data.createCommitOnBranch.commit.oid' "$work/response.json")
echo "Committed ${oid} to ${branch}: ${headline}"

# Bring the checkout to the commit GitHub made, so later steps build on it.
git fetch --quiet origin "$branch"
git reset --quiet --hard "$oid"
