#!/usr/bin/env bash
# Copyright 2026 Kubotal
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

# Shared helpers + per-developer overrides from dev.env (if present).
# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# Move to the repository root directory to ensure relative paths are stable
cd "$(dirname "$0")/.."

# Configuration
reg_name='kubocd-registry'
reg_port="${KUBOCD_REGISTRY_PORT:-5001}"
reg_image='registry:2.8.3'
cluster_name='kubocd'
kind_config="hack/kind-config.yaml"

# Helper: Retry function for network-dependent commands
retry() {
  local n=1
  local max=3
  local delay=5
  while true; do
    if "$@"; then
      return 0
    else
      if (( n >= max )); then
        echo "Error: Command '$*' failed after $n attempts." >&2
        return 1
      else
        echo "Warning: Command '$*' failed. Retrying in $delay seconds (Attempt $n/$max)..." >&2
        sleep $delay
        ((n++))
      fi
    fi
  done
}

# 0. Pre-flight checks
echo "Running pre-flight environment checks..."

# Check Docker daemon
if ! docker info >/dev/null 2>&1; then
  echo "Error: The Docker daemon is not running or not reachable. Please start Docker and try again." >&2
  exit 1
fi

# Check required binaries in PATH
for cmd in kind kubectl flux kustomize curl; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Error: Required tool '$cmd' is not installed in the PATH." >&2
    exit 1
  fi
done

# Is our registry already running? Computed once (the daemon, checked above, is
# required) and reused below: it lets us skip the port check when we already
# hold the port, and skip re-creating the container (idempotent re-run).
reg_running="false"
if [ "$(docker inspect -f '{{.State.Running}}' "${reg_name}" 2>/dev/null || true)" = "true" ]; then
  reg_running="true"
fi

# Check the registry host port is free before we try to bind it. Under
# Docker-outside-of-Docker the registry publishes on the *host's* loopback
# (127.0.0.1:${reg_port}), so the Docker daemon — not an in-container TCP probe,
# which would inspect the wrong network namespace — is the only reliable
# authority. We reuse the pinned registry image as a throwaway bind-probe (no
# extra dependency); a non-conflict error (e.g. image pull) is ignored here and
# left for the real 'docker run' below to surface and retry. Skipped when our
# own registry already holds the port.
if [ "${reg_running}" = "false" ]; then
  probe_err="$(docker run --rm --entrypoint /bin/true \
    -p "127.0.0.1:${reg_port}:${reg_port}" "${reg_image}" 2>&1 >/dev/null || true)"
  if printf '%s' "${probe_err}" | grep -q 'port is already allocated'; then
    echo "Error: host port ${reg_port} is already in use on this machine." >&2
    echo "Pick a free port via KUBOCD_REGISTRY_PORT in dev.env, then re-run." >&2
    echo "(If a previous KuboCD registry is still bound, run 'make dev-down' first.)" >&2
    exit 1
  fi
fi

echo "Pre-flight checks passed successfully!"

# 1. Create OCI local registry container if it does not exist
if [ "${reg_running}" = "false" ]; then
  echo "Creating OCI local registry container: ${reg_name}..."
  # Pin the registry container image to a specific version for reproducibility
  # Retry covers transient pull failures from Docker Hub
  retry docker run \
    -d --restart=always -p "127.0.0.1:${reg_port}:${reg_port}" --network bridge \
    -e "REGISTRY_HTTP_ADDR=:${reg_port}" \
    --name "${reg_name}" \
    "${reg_image}"
else
  echo "Registry container ${reg_name} is already running."
fi

# Active healthcheck: Wait until the local registry is responsive before proceeding
echo "Waiting for OCI registry to be fully responsive..."
reg_ready=false
for _ in $(seq 1 30); do
  if docker exec "${reg_name}" wget -q --spider "http://localhost:${reg_port}/v2/" >/dev/null 2>&1; then
    reg_ready=true
    break
  fi
  sleep 1
done

if [ "$reg_ready" = "false" ]; then
  echo "Error: Local registry container started but did not respond on its internal port ${reg_port} within 30 seconds." >&2
  exit 1
fi
echo "OCI registry container is ready."

# 2. Create Kind cluster using our custom containerd config patches if it does not exist
if ! kind get clusters | grep -q "^${cluster_name}$"; then
  echo "Creating Kind cluster named '${cluster_name}'..."
  retry kind create cluster --name "${cluster_name}" --config "${kind_config}" --wait 5m
else
  echo "Kind cluster '${cluster_name}' already exists."
fi

# Patch kubeconfig: from the devcontainer, the apiserver port is reachable via host.docker.internal,
# and the cert validates against the SAN '${cluster_name}-control-plane'.
echo "Patching kubeconfig for devcontainer access..."
api_port=$(docker port "${cluster_name}-control-plane" 6443/tcp | head -1 | awk -F: '{print $2}')
kubectl config set-cluster "kind-${cluster_name}" \
  --server="https://host.docker.internal:${api_port}" \
  --tls-server-name="${cluster_name}-control-plane"

# 3. Connect the registry container to the Kind cluster network if not connected
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${reg_name}")" = "null" ]; then
  echo "Connecting registry ${reg_name} to the kind Docker network..."
  retry docker network connect "kind" "${reg_name}"
fi

# 4. Document the local registry hosting inside the cluster
# This tells tools like Flux/Helm where the OCI registry is located
echo "Applying local registry hosting ConfigMap to kube-public..."
# Note: the manifest is materialized to a temp file before being applied via retry.
# Piping a heredoc directly into 'retry kubectl apply -f -' consumes stdin on the
# first attempt; subsequent retries would receive empty input and silently no-op.
cat <<EOF > /tmp/local-registry-hosting.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${reg_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
retry kubectl apply --context "kind-${cluster_name}" -f /tmp/local-registry-hosting.yaml
rm -f /tmp/local-registry-hosting.yaml

# 5. Configure containerd on each cluster node to route localhost:5001 to our local registry container
echo "Configuring containerd registry routing on Kind nodes..."
for node in $(kind get nodes --name "${cluster_name}"); do
  # Create standard containerd directory for localhost:5001 certs/routing
  docker exec "${node}" mkdir -p "/etc/containerd/certs.d/localhost:${reg_port}"
  
  # Inject the hosts.toml mapping inside the node
  docker exec -i "${node}" sh -c "cat > /etc/containerd/certs.d/localhost:${reg_port}/hosts.toml" <<EOF
server = "http://${reg_name}:${reg_port}"

[host."http://${reg_name}:${reg_port}"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOF
done

# 6. Bootstrap Flux inside the Kind cluster
if ! kubectl --context "kind-${cluster_name}" get namespace flux-system >/dev/null 2>&1; then
  echo "Bootstrapping Flux (system controllers) into the cluster..."
  retry flux install --context "kind-${cluster_name}"
else
  echo "Flux is already installed in the cluster."
fi

# 7. Install KuboCD CRDs into the cluster
echo "Installing KuboCD CRDs into the cluster..."
# Note: same stdin-consumption rationale as step 4 — materialize the rendered
# CRDs to a temp file so retries see consistent input on subsequent attempts.
kustomize build config/crd > /tmp/kubocd-crds.yaml
retry kubectl --context "kind-${cluster_name}" apply -f /tmp/kubocd-crds.yaml
rm -f /tmp/kubocd-crds.yaml

# 8. Wait for core resources to be fully ready
echo "Waiting for KuboCD CRDs to be established..."
for f in config/crd/bases/*.yaml; do
  crd_name=$(awk '/^metadata:/ {in_meta=1} /^spec:/ {in_meta=0} in_meta && /^[[:space:]]+name:/ {print $2; exit}' "$f")
  if [ -n "$crd_name" ]; then
    kubectl --context "kind-${cluster_name}" wait --for=condition=Established --timeout=60s "crd/${crd_name}"
  fi
done

echo "Waiting for Flux controllers to be available..."
kubectl --context "kind-${cluster_name}" -n flux-system wait \
  deploy/helm-controller \
  deploy/kustomize-controller \
  deploy/notification-controller \
  deploy/source-controller \
  --for condition=Available --timeout=90s

echo "Idempotent development cluster and OCI registry successfully initialized!"
echo "Cluster context: kind-${cluster_name}"
echo "Local Registry: localhost:${reg_port}"
