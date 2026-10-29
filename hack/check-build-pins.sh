#!/usr/bin/env bash
# Check workflow action SHAs and version comments and production image digests.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export LC_ALL=C
shopt -s nullglob
action='[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*@[[:xdigit:]]{40}[[:space:]]+# v[0-9]+\.[0-9]+\.[0-9]+'
local_action='\./[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*([[:space:]]+#.*)?'
uses_key="[\"']?uses[\"']?[[:space:]]*:"
uses="^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+($action|$local_action)[[:space:]]*$"
from='^[[:space:]]*[Ff][Rr][Oo][Mm]([[:space:]]|$)'
image='[A-Za-z0-9./_:-]+:[A-Za-z0-9_][A-Za-z0-9_.-]*@sha256:[[:xdigit:]]{64}'
base="^[[:space:]]*[Ff][Rr][Oo][Mm][[:space:]]+(--platform=[^[:space:]]+[[:space:]]+)?$image([[:space:]]+[Aa][Ss][[:space:]]+[A-Za-z0-9_.-]+)?[[:space:]]*$"
failed=0 actions=0 images=0
fail() {
    printf '%s:%s: %s\n' "$1" "$2" "$3" >&2
    failed=1
}
for file in .github/workflows/*.yml .github/workflows/*.yaml Dockerfile; do
    number=0
    while IFS= read -r line || [[ -n $line ]]; do
        number=$((number + 1))
        if [[ $file == Dockerfile ]]; then
            [[ $line =~ $from ]] || continue
            images=$((images + 1))
            pattern=$base message='FROM requires image:tag@sha256:<64 hex>'
        else
            [[ $line =~ $uses_key ]] || continue
            actions=$((actions + 1))
            pattern=$uses message='uses requires owner/repo[/path]@<40 hex> # vX.Y.Z or ./path'
        fi
        if [[ ! $line =~ $pattern ]]; then
            printf '%s:%s: %s\n' "$file" "$number" "$message" >&2
            failed=1
        fi
    done < "$file"
done
[[ $actions -gt 0 ]] || fail .github/workflows 1 'empty action inventory'
[[ $images -gt 0 ]] || fail Dockerfile 1 'empty production base-image inventory'
[[ $failed -eq 0 ]] || exit 1
printf 'Build pins checked: %d actions, %d production base images\n' "$actions" "$images"
