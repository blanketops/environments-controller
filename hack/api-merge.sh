#!/usr/bin/env bash
# Merges one branch into another through GitHub's API, always as a merge
# commit, and checks the result out.
#
# The merge commit is made and signed by GitHub, so it is verified. See
# api-commit.sh for why the release workflows do not use git for this.
#
# Usage: api-merge.sh <base> <head> <message>
# Needs: GH_TOKEN, GITHUB_REPOSITORY, curl, jq.
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

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

jq -n --arg base "$base" --arg head "$head" --arg message "$message" \
  '{base: $base, head: $head, commit_message: $message}' >"$work/request.json"

status=$(curl -sS -o "$work/response.json" -w '%{http_code}' \
  -X POST \
  -H "Authorization: bearer ${GH_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  --data-binary "@$work/request.json" \
  "${api}/repos/${GITHUB_REPOSITORY}/merges")

case "$status" in
  201) echo "Merged ${head} into ${base}: $(jq -r '.sha' "$work/response.json")" ;;
  204) echo "${base} already contains ${head}; nothing to merge" ;;
  *)
    echo "::error::merging ${head} into ${base} failed with HTTP ${status}" >&2
    jq -r '.message // empty' "$work/response.json" >&2
    exit 1
    ;;
esac

git fetch --quiet origin "$base"
git checkout --quiet -B "$base" "origin/$base"
