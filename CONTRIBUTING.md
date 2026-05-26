# Contributing to KuboCD

This guide details the setup and workflows for the local development environment of KuboCD.

---

## 1. Architecture Overview

The KuboCD development environment is based on:

1. **Native Go Tooling (Go 1.24+)**: Go-based tools (`golangci-lint`, `controller-gen`, `kustomize`) are declared as module-level dependencies directly inside `go.mod`. Go downloads and runs the exact pinned version automatically via `go tool` commands. Inside the Devcontainer, you do not need to install any toolchains or system-wide binaries.

2. **Docker-outside-of-Docker (DooD)**: The Devcontainer mounts the host's Docker socket (`/var/run/docker.sock`). This allows reusing host-level Docker resources, sharing image caches, and simplifies container communication.
3. **Pre-cached Envtest Assets**: The Devcontainer pre-packages the Kubernetes control plane assets (`kube-apiserver`, `etcd`) in `/usr/local/share/envtest` and configures `KUBEBUILDER_ASSETS`. This avoids downloading Kubernetes binaries at runtime during test execution.
4. **Idempotent Local Infrastructure**: A single command (`make dev-up`) brings up a local registry container, a Kind cluster connected to the same network, containerd registry mirrors, and bootstraps Flux.


---

## 2. Onboarding Workflow

### Step 1: Clone and Open in Devcontainer
1. Clone the repository:
   ```bash
   git clone https://github.com/kubocd/kubocd.git
   cd kubocd
   ```
2. Open the directory in **VS Code** or **Cursor**.
3. When prompted, select **"Reopen in Container"** (or run `Dev Containers: Reopen in Container` from the command palette).
4. Once the container is built, the environment is ready with Go, kubectl, helm, kind, and flux CLI pre-installed.

### Step 2: Initialize Local Infrastructure
Within the Devcontainer terminal, run:
```bash
make dev-up
```
This target will:
* Boot up a local OCI registry container (`kubocd-registry` on `localhost:5001`).
* Create a Kind Kubernetes cluster named `kubocd`.
* Connect the registry to the cluster network and patch `containerd` config routing.
* Bootstrap **Flux** in the cluster.
* Install **KuboCD CRDs** into the cluster.

---

## 3. Daily Development Cycle (Inner Loop)

### Edit Code
Modify files inside `internal/`, `api/`, `cmd/`, or `helm/`.

### Run KuboCD Locally
To launch the controller process in your terminal against the Kind cluster:
```bash
make run
```
*Note: `ENABLE_WEBHOOKS=false` is set by default in this mode because the API server inside the cluster cannot reach the webhook server on your host machine without tunneling. To test webhooks, deploy the controller inside the cluster.*

### Run Tests
The tests use envtest. Since the assets are pre-cached, running tests does not require downloading Kubernetes binaries:
```bash
make test
```

### Run Linter
To execute the linter:
```bash
make lint
# Or to automatically apply safe fixes:
make lint-fix
```

### Clean Up Infrastructure
To tear down the Kind cluster and local registry container:
```bash
make dev-down
```
*Note: Both `make dev-up` and `make dev-down` are idempotent.*

### In-Cluster Deployment & Testing
To test the controller running completely inside the Kind cluster (allowing full webhook integration), you can build, tag, and push the manager container image to the local OCI registry:

1. **Build and Tag Image**:
   Build the controller manager image pointing to the local registry namespace:
   ```bash
   make docker-build IMG=localhost:5001/kubocd:dev
   ```

2. **Push to Local Registry**:
   Push the image to the local OCI registry (since the registry maps to `localhost:5001`, standard Docker pushes will work seamlessly):
   ```bash
   docker push localhost:5001/kubocd:dev
   ```

3. **Deploy inside Kind**:
   Deploy the controller and manifests into the cluster using Kustomize and the pushed image:
   ```bash
   make build-installer IMG=localhost:5001/kubocd:dev
   kubectl apply -f config/_dist_/install.yaml
   ```

---


## 4. Alternative: Native Host Development

If you prefer to develop directly on your host machine instead of using the Devcontainer, you must satisfy the following prerequisites manually:
1. **Toolchain**: Install **Go 1.25** on your host. Module-level tools (`golangci-lint`, `controller-gen`, `kustomize`) will still be managed automatically by the Go toolchain via `go tool` commands in the `Makefile`.
2. **System CLIs**: Install `kubectl`, `helm`, `kind`, and `flux` globally on your host. The exact recommended versions are declared in the `.tool-versions` file at the root of the repository (which can be used to install them automatically using **asdf** or **mise**).
3. **Envtest Cache**: Run `make setup-envtest` to dynamically fetch and cache the required Kubernetes control plane assets in your host's `bin/` directory before running `make test`.


