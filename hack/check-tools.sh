#!/usr/bin/env bash
# Check that locally installed dev tools match the versions pinned in
# .tool-versions. Prints a per-tool report and exits non-zero if any tool is
# missing or mismatched. No 'set -e': probing absent tools is expected.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

if [ ! -f "${TOOL_VERSIONS_FILE}" ]; then
    echo "❌ .tool-versions file not found!"
    exit 1
fi

echo "Checking local development tools against .tool-versions..."
failed=0

# check_tool <label> <required> <installed>
check_tool() {
    local label="$1" req="$2" cur="$3"
    if [ -z "$cur" ]; then
        echo "❌ ${label} is NOT installed! (Required: ${req})"
        return 1
    elif [ "$req" != "$cur" ]; then
        echo "❌ ${label} version mismatch! (Required: ${req}, Installed: ${cur})"
        return 1
    fi
    echo "✅ ${label} version matches (${cur})"
    return 0
}

major_minor() { echo "$1" | awk -F. '{ print $1"."$2 }'; }

# Go: check MAJOR.MINOR only (patch version floats with base image)
tool_go_full="$(tool_version golang)"
tool_go_minor="$(major_minor "$tool_go_full")"
installed_go="$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')"
installed_go_minor="$(major_minor "$installed_go")"

if [ -z "$installed_go" ]; then
    echo "❌ Go is NOT installed! (Required: ${tool_go_minor} from .tool-versions)"
    failed=1
elif [ "$tool_go_minor" != "$installed_go_minor" ]; then
    echo "❌ Go MAJOR.MINOR mismatch! (Required: ${tool_go_minor}, Installed: ${installed_go_minor}; full versions: .tool-versions=${tool_go_full}, installed=${installed_go})"
    failed=1
else
    echo "✅ Go MAJOR.MINOR matches (${tool_go_minor}; full versions: .tool-versions=${tool_go_full}, installed=${installed_go})"
fi
check_tool "kubectl" "$(tool_version kubectl)" \
    "$(kubectl version --client 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)" || failed=1
check_tool "Helm" "$(tool_version helm)" \
    "$(helm version --template='{{.Version}}' 2>/dev/null | sed 's/^v//')" || failed=1
check_tool "Kind" "$(tool_version kind)" \
    "$(kind version 2>/dev/null | awk '{print $2}' | sed 's/^v//')" || failed=1
check_tool "Flux" "$(tool_version flux)" \
    "$(flux version --client 2>/dev/null | awk '/flux/ {print $2}' | sed 's/^v//')" || failed=1

exit "$failed"
