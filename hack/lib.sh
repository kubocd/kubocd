#!/usr/bin/env bash
# Shared helpers for hack/ scripts. Source this file; do not execute it.

# Repo root resolved from this file's location (hack/ sits directly under root).
_hack_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_hack_lib_dir}/.." && pwd)"
TOOL_VERSIONS_FILE="${TOOL_VERSIONS_FILE:-${REPO_ROOT}/.tool-versions}"

# Load per-developer overrides from dev.env if present (git-ignored). Values are
# auto-exported so child processes inherit them. An absent file is a no-op; this
# never fails. Same file on the host and in the devcontainer (repo is mounted).
DEV_ENV_FILE="${DEV_ENV_FILE:-${REPO_ROOT}/dev.env}"
if [ -f "${DEV_ENV_FILE}" ]; then
    set -a
    # shellcheck disable=SC1090
    . "${DEV_ENV_FILE}"
    set +a
fi

# tool_version <name>: print the version pinned for <name> in .tool-versions
# (matched on the exact first field). Empty output if the tool is absent.
tool_version() {
    awk -v t="$1" '$1 == t { print $2 }' "${TOOL_VERSIONS_FILE}"
}
