#!/usr/bin/env bash
# Tags the checked out commit with a version and pushes the tag to origin, before its artifacts are published.
#
# A version with a pre-release part, such as v0.4.1-snapshot, is a snapshot: its tag moves to each published commit.
# A final version, such as v0.4.1, never moves its tag: the first publication creates it, a run again on the same commit
# keeps it, and a tag on another commit stops the publication.
#
# Prints snapshot=true or snapshot=false on stdout, for $GITHUB_OUTPUT, and everything else on stderr.
#
# Usage: release-tag.sh <version>
set -euo pipefail

version=${1:?usage: release-tag.sh <version>}
# A SemVer version with a v, without build metadata, as a Docker tag cannot hold a "+".
number='(0|[1-9][0-9]*)'
identifier='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
if [[ ! $version =~ ^v$number\.$number\.$number(-$identifier(\.$identifier)*)?$ ]]; then
  echo "::error::'$version' is not a version such as v0.4.1 or v0.4.1-snapshot" >&2
  exit 1
fi

if [[ $version == *-* ]]; then
  git tag -f "$version" >&2
  git push -f origin "refs/tags/$version" >&2
  echo "snapshot=true"
  exit 0
fi

# The last line gives the commit of an annotated tag, or the only one of a lightweight tag.
tagged=$(git ls-remote origin "refs/tags/$version" "refs/tags/$version^{}" | tail -n 1 | cut -f 1)
commit=$(git rev-parse HEAD)
if [[ -z $tagged ]]; then
  # Without force, the push refuses a tag that another run created meanwhile.
  git tag "$version" >&2
  git push origin "refs/tags/$version" >&2
elif [[ $tagged != "$commit" ]]; then
  echo "::error::The tag $version is on $tagged: a final version never moves its tag, so $commit is not published" >&2
  exit 1
fi
echo "snapshot=false"
