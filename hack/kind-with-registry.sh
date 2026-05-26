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

# Move to the repository root directory to ensure relative paths are stable
cd "$(dirname "$0")/.."

# Configuration
reg_name='kubocd-registry'
reg_port='5001'
cluster_name='kubocd'
kind_config="hack/kind-config.yaml"


# 1. Create OCI local registry container if it does not exist
if [ "$(docker inspect -f '{{.State.Running}}' "${reg_name}" 2>/dev/null || true)" != "true" ]; then
  echo "Creating OCI local registry container: ${reg_name}..."
  docker run \
    -d --restart=always -p "127.0.0.1:${reg_port}:5000" --network bridge --name "${reg_name}" \
    registry:2
else
  echo "Registry container ${reg_name} is already running."
fi

# 2. Create Kind cluster using our custom containerd config patches if it does not exist
if ! kind get clusters | grep -q "^${cluster_name}$"; then
  echo "Creating Kind cluster named '${cluster_name}'..."
  kind create cluster --name "${cluster_name}" --config "${kind_config}" --wait 5m
else
  echo "Kind cluster '${cluster_name}' already exists."
fi

# 3. Connect the registry container to the Kind cluster network if not connected
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${reg_name}")" = "null" ]; then
  echo "Connecting registry ${reg_name} to the kind Docker network..."
  docker network connect "kind" "${reg_name}"
fi

# 4. Document the local registry hosting inside the cluster
# This tells tools like Flux/Helm where the OCI registry is located
echo "Applying local registry hosting ConfigMap to kube-public..."
cat <<EOF | kubectl apply --context "kind-${cluster_name}" -f -
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

# 5. Configure containerd on each cluster node to route localhost:5001 to our local registry container
echo "Configuring containerd registry routing on Kind nodes..."
for node in $(kind get nodes --name "${cluster_name}"); do
  # Create standard containerd directory for localhost:5001 certs/routing
  docker exec "${node}" mkdir -p "/etc/containerd/certs.d/localhost:${reg_port}"
  
  # Inject the hosts.toml mapping inside the node
  docker exec -i "${node}" sh -c "cat > /etc/containerd/certs.d/localhost:${reg_port}/hosts.toml" <<EOF
server = "http://${reg_name}:5000"

[host."http://${reg_name}:5000"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOF
done

# 6. Bootstrap Flux inside the Kind cluster
if ! kubectl --context "kind-${cluster_name}" get namespace flux-system >/dev/null 2>&1; then
  echo "Bootstrapping Flux (system controllers) into the cluster..."
  flux install --context "kind-${cluster_name}"
else
  echo "Flux is already installed in the cluster."
fi

# 7. Install KuboCD CRDs into the cluster
echo "Installing KuboCD CRDs into the cluster..."
go tool kustomize build config/crd | kubectl --context "kind-${cluster_name}" apply -f -

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

