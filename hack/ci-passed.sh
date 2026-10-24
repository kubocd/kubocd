#!/usr/bin/env bash
# Succeeds when the workflow ci.yml passed on a push of the commit to main, in the repository $GITHUB_REPOSITORY. A run
# on a pull request tests a merge commit, not the commit itself, so it does not count.
#
# Usage: ci-passed.sh <commit>
set -euo pipefail

commit=${1:?usage: ci-passed.sh <commit>}
runs=$(gh run list --repo "${GITHUB_REPOSITORY:?}" --workflow ci.yml --commit "$commit" --event push --branch main \
  --status success --json databaseId --jq length)
if [[ ! $runs =~ ^[0-9]+$ ]] || ((runs == 0)); then
  echo "::error::The CI did not pass on a push of $commit to main, so it is not published" >&2
  exit 1
fi
