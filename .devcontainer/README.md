# KuboCD Local Dev Environment (Devcontainer)

Pre-configured with all system packages, compiler toolchains, Kubernetes
utilities, and the envtest toolchain.

## Architecture Overview

The environment uses a **Docker-outside-of-Docker (DooD)** setup:

1. **Dev Container**: Your development tools and terminal run inside a
   Debian-based container (`devcontainer`).
2. **Host Docker Daemon**: The container accesses the host machine's Docker
   daemon via a mounted Unix socket (`/var/run/docker.sock`).
3. **Local OCI Registry**: A containerized registry (`kubocd-registry`,
   `registry:2.8.3`) runs as a **sibling container on the host Docker daemon —
   not inside the devcontainer**. Its port is published to the host at
   `127.0.0.1:5001` (default; configurable via `dev.env`). Kind nodes pull from
   it internally over the shared `kind` network (`kubocd-registry:5001`).
4. **Local Kind Cluster**: A Kubernetes cluster named `kubocd` is created using
   Kind. It runs directly as a sibling container on the host Docker daemon,
   making it accessible from both the host and the devcontainer.

## Mounted Caches

Persistent named Docker volumes cache Go modules and build output:

- `kubocd-gomodcache` persists Go modules under `/home/vscode/go/pkg/mod`.
- `kubocd-gobuildcache` persists compiled objects under
  `/home/vscode/.cache/go-build`.

## Pre-cached Tools & Verification

All tool versions are pinned in [`.tool-versions`](../.tool-versions) as the
single source of truth — see that file for the exact versions. At build time:

- **kubectl, Helm, Kind, Flux, Kustomize, k9s and Node.js** are downloaded and
  verified against their official SHA256 checksums.
- **Go** comes from the base image (`go:dev-1.25-bookworm`); its patch floats to
  the latest 1.25 release, frozen at runtime by `GOTOOLCHAIN=local`.
- **Prettier** (npm) and **pre-commit** (pip) are version-pinned installs.
- **setup-envtest** assets are pre-cached, pinned to a commit on the
  `release-0.23` branch for Go 1.25 compatibility (published v0.24.x tags
  require Go 1.26).

The Kind apiserver is reachable from inside the devcontainer via
`host.docker.internal` — the kubeconfig is patched at `make dev-up` time (Docker
Desktop natively, host-gateway alias on Linux via `runArgs`).

The OCI registry follows the same path: from inside the devcontainer, reach it
at `host.docker.internal:5001`. `localhost:5001` works only from the host shell
or for `docker` commands — those execute on the host daemon, not in the
container.

A `pre-commit` hook is wired automatically (via `postCreateCommand`): it
hard-wraps Markdown at 80 columns with Prettier on every commit.

## Quick Start (in your editor)

1. Open this repository in any Dev Containers-capable editor (VS Code, Cursor,
   JetBrains, Antigravity, …) and let it reopen/start in the container.
2. Open a terminal inside the container and run `make dev-up`.
3. Tear down with `make dev-down`.

## Running it without an IDE (Dev Containers CLI)

The same container can be built and driven from the command line — no IDE
required:

```bash
# install the CLI once (Node.js must be available on your host)
npm install -g @devcontainers/cli

# build + start the container from the repo root
devcontainer up --workspace-folder .

# run anything inside it
devcontainer exec --workspace-folder . make dev-up
devcontainer exec --workspace-folder . make test
```

> Give Docker enough RAM/CPU — the local stack (kind + Flux + OCI registry)
> needs headroom to come up.

## Customizing your environment (`dev.env`)

Per-developer overrides live in a git-ignored `dev.env` at the repo root. Copy
the template and edit it:

```bash
cp dev.env.example dev.env
```

`dev.env` is sourced by the `hack/` scripts (e.g. `make dev-up`), by the
Makefile, and by the container shell — so it applies identically **on the host
and inside the devcontainer**. With no `dev.env` file, the documented defaults
are used; there is nothing to configure to get started.

Available settings:

- `KUBOCD_REGISTRY_PORT` — host port for the local OCI registry (default
  `5001`). After changing it, run `make dev-down` before `make dev-up` so the
  registry is recreated on the new port.

## Troubleshooting

- **Docker socket permissions**: If you get a "permission denied" error when
  running `docker` commands inside the container, make sure your host Docker
  daemon is running and that your user has appropriate permissions to write to
  `/var/run/docker.sock`.
- **Registry port conflict**: `make dev-up` runs a pre-flight check that fails
  fast — naming the port — if the registry's host port (`5001` by default) is
  already taken. The probe goes through the Docker daemon, since the port is
  published on the host's loopback. Either stop the conflicting process, or set
  a different `KUBOCD_REGISTRY_PORT` in `dev.env` (see _Customizing your
  environment_), then re-run `make dev-up`. If the holder is a leftover KuboCD
  registry, run `make dev-down` first.
