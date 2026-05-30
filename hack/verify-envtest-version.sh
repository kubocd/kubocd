#!/usr/bin/env bash
# Verify the active envtest assets (KUBEBUILDER_ASSETS) match the Kubernetes
# version expected by go.mod (ENVTEST_K8S_VERSION). No-op if KUBEBUILDER_ASSETS
# is unset (envtest is then fetched on demand by 'make setup-envtest').
# Run via 'make verify-envtest-version'.
set -euo pipefail

[ -n "${KUBEBUILDER_ASSETS:-}" ] || exit 0
: "${ENVTEST_K8S_VERSION:?must be set (derived from go.mod k8s.io/api; run via 'make verify-envtest-version')}"

target="${KUBEBUILDER_ASSETS}"
while [ -L "$target" ]; do
    target="$(readlink "$target")"
done
dir_name="$(basename "$target")"

case "$dir_name" in
    "${ENVTEST_K8S_VERSION}"*)
        echo "Envtest version verified: $dir_name matches expected ${ENVTEST_K8S_VERSION}*"
        ;;
    *)
        echo "ERROR: Envtest version mismatch!"
        echo "  Active assets path: ${KUBEBUILDER_ASSETS} (resolves to $target)"
        echo "  Expected version prefix: ${ENVTEST_K8S_VERSION}* (derived from go.mod k8s.io/api)"
        exit 1
        ;;
esac
