#!/usr/bin/env bash
# Check locally installed dev tools against the versions pinned in .tool-versions.
# A tool is reported only when something is off:
#   - missing          -> error   (script exits non-zero)
#   - version mismatch -> warning (non-fatal; lower or higher both warn)
#   - exact match      -> silent
# No 'set -e': probing absent tools is expected.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

if [ ! -f "${TOOL_VERSIONS_FILE}" ]; then
    echo "❌ .tool-versions file not found!" >&2
    exit 1
fi

failed=0

semver() { grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1; }
minor() { awk -F. '{ print $1"."$2 }'; }

# report <label> <pinned> <installed>
#   empty <installed> => not installed.
report() {
    local label="$1" pinned="$2" installed="$3"
    [ -z "$pinned" ] && return 0 # not pinned in .tool-versions; nothing to check
    if [ -z "$installed" ]; then
        echo "❌ ${label}: not installed (pinned ${pinned})" >&2
        failed=1
    elif [ "$pinned" != "$installed" ]; then
        echo "⚠️  ${label}: ${installed} installed, ${pinned} pinned" >&2
    fi
    # exact match -> no output
}

# Go is special: compare MAJOR.MINOR only (the patch floats with the base image).
go_minor="$(tool_version golang | minor)"
go_installed="$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')"
if [ -n "$go_minor" ]; then
    if [ -z "$go_installed" ]; then
        echo "❌ go: not installed (pinned ${go_minor}.x)" >&2
        failed=1
    elif [ "$go_minor" != "$(printf '%s' "$go_installed" | minor)" ]; then
        echo "⚠️  go: ${go_installed} installed, ${go_minor}.x pinned" >&2
    fi
fi

report kubectl "$(tool_version kubectl)" "$(kubectl version --client 2>/dev/null | semver)"
report helm "$(tool_version helm)" "$(helm version --template='{{.Version}}' 2>/dev/null | semver)"
report kind "$(tool_version kind)" "$(kind version 2>/dev/null | semver)"
report flux "$(tool_version flux)" "$(flux version --client 2>/dev/null | semver)"
report kustomize "$(tool_version kustomize)" "$(kustomize version 2>/dev/null | semver)"
report k9s "$(tool_version k9s)" "$(k9s version 2>/dev/null | semver)"
report nodejs "$(tool_version nodejs)" "$(node --version 2>/dev/null | semver)"
report prettier "$(tool_version prettier)" "$(prettier --version 2>/dev/null | semver)"
report pre-commit "$(tool_version pre-commit)" "$(pre-commit --version 2>/dev/null | semver)"

exit "$failed"
