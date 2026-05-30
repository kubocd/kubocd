#!/usr/bin/env bash
# Verify .tool-versions and go.mod agree on the Go and Kubernetes MAJOR.MINOR
# versions. ENVTEST_K8S_VERSION must be in the environment (the Makefile derives
# it from go.mod's k8s.io/api). Run via 'make verify-tool-versions'.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# Mirror the original Make recipe: silently succeed if .tool-versions is absent.
[ -f "${TOOL_VERSIONS_FILE}" ] || exit 0

: "${ENVTEST_K8S_VERSION:?must be set (derived from go.mod k8s.io/api; run via 'make verify-tool-versions')}"

major_minor() { echo "$1" | awk -F. '{ print $1"."$2 }'; }

tool_go_full="$(tool_version golang)"
tool_go_minor="$(major_minor "$tool_go_full")"
go_mod_minor="$(grep -E '^go [0-9]+\.[0-9]+' "${REPO_ROOT}/go.mod" | awk '{print $2}' | awk -F. '{print $1"."$2}')"

if [ "$tool_go_minor" != "$go_mod_minor" ]; then
    echo "ERROR: Go MAJOR.MINOR mismatch between .tool-versions and go.mod!"
    echo "  .tool-versions: golang $tool_go_full (MAJOR.MINOR = $tool_go_minor)"
    echo "  go.mod: go $go_mod_minor"
    exit 1
fi

tool_k8s_full="$(tool_version kubectl)"
tool_k8s_minor="$(major_minor "$tool_k8s_full")"

if [ "$tool_k8s_minor" != "${ENVTEST_K8S_VERSION}" ]; then
    echo "ERROR: Kubernetes MAJOR.MINOR mismatch between .tool-versions and go.mod k8s.io/api!"
    echo "  .tool-versions: kubectl $tool_k8s_full (MAJOR.MINOR = $tool_k8s_minor)"
    echo "  go.mod (k8s.io/api -> ENVTEST_K8S_VERSION): ${ENVTEST_K8S_VERSION}"
    exit 1
fi

echo "Tool versions verified successfully: golang $tool_go_full, kubectl $tool_k8s_full"
