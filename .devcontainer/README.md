# KuboCD Local Dev Environment (Devcontainer)

Welcome to the KuboCD local development environment! This environment is pre-configured with all necessary system packages, compiler toolchains, Kubernetes utilities, and test suites.

## Architecture Overview

To support a seamless and performant development experience, the environment uses a **Docker-outside-of-Docker (DooD)** setup:
1. **VS Code Container**: The development IDE and terminal run inside a Debian-based container (`devcontainer`).
2. **Host Docker Daemon**: The container accesses the host machine's Docker daemon via a mounted Unix socket (`/var/run/docker.sock`).
3. **Local OCI Registry**: A local containerized registry is launched on port `5001` to serve OCI Helm charts and Docker images.
4. **Local Kind Cluster**: A Kubernetes cluster named `kubocd` is created using Kind. It runs directly as a sibling container on the host Docker daemon, making it accessible from both the host and the devcontainer.

## Mounted Caches

To ensure rebuilding the devcontainer is extremely fast, persistent named Docker volumes are configured for Go caches:
- `kubocd-gomodcache` persists Go modules under `/home/vscode/go/pkg/mod`.
- `kubocd-gobuildcache` persists compiled objects under `/home/vscode/.cache/go-build`.

## Pre-cached Tools & Verification

All binaries are installed deterministically matching `.tool-versions` as the single source of truth, and cryptographically verified using official SHA256 checksums at build time:
- `golang 1.25.0` (with frozen `GOTOOLCHAIN=local` to prevent unexpected silent upgrades)
- `kubectl 1.34.0`
- `helm 3.16.2`
- `kind 0.31.0`
- `flux 2.8.8`
- `kustomize 5.8.1`
- `setup-envtest` (pinned to commit `f9589b9f` on the `release-0.23` branch for Go 1.25 compatibility; published v0.24.x tags require Go 1.26)

The Kind apiserver is reachable from inside the devcontainer via `host.docker.internal` — the kubeconfig is patched at `make dev-up` time (Docker Desktop natively, host-gateway alias on Linux via `runArgs`).

## Quick Start (Bootstrap)

1. Open this repository inside VS Code.
2. When prompted, click **"Reopen in Container"** (or use the Command Palette `F1` and select `Dev Containers: Reopen in Container`).
3. Once loaded, open a terminal inside the container and spin up the development cluster and registry:
   ```bash
   make dev-up
   ```
4. To clean up and delete the cluster/registry:
   ```bash
   make dev-down
   ```

## Troubleshooting

- **Docker socket permissions**: If you get a "permission denied" error when running `docker` commands inside the container, make sure your host Docker daemon is running and that your user has appropriate permissions to write to `/var/run/docker.sock`.
- **Registry Port conflict**: If port `5001` is already in use by another local application, the registry will fail to start. You can stop the conflicting application or customize the port.
