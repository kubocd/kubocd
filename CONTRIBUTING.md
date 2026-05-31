# Contributing to KuboCD

Setting up a local KuboCD development environment.

## Tooling

Tool versions are pinned in [`.tool-versions`](.tool-versions) (Go 1.25,
`kubectl`, `kind`, `flux`, `kustomize`, …).

- **💡 Recommended — Devcontainer:** every tool, binary, and cache provisioned
  for you. See [`.devcontainer/README.md`](.devcontainer/README.md) to open it
  (any Dev Containers-capable editor, or the headless CLI).
- **Native host:** install the `.tool-versions` set with `mise` or `asdf`.

## Code standards

A `pre-commit` hook hard-wraps Markdown to 80 columns with Prettier — pre-wired
in the Devcontainer. On the host, put `pre-commit` and `prettier` on your PATH
(`brew install pre-commit prettier`, or `mise`/`asdf` from `.tool-versions`),
then run `pre-commit install` once. If a commit is rejected after
auto-reformatting, re-stage and commit again.

## Common commands

A few `make` targets to get around — run `make help` for the full list:

```bash
make dev-up    # local registry + Kind cluster + Flux + CRDs (idempotent)
make run       # run the controller against the cluster
make test      # unit tests (envtest)
make lint-fix  # lint with safe autofixes
make dev-down  # tear everything down
```

Override defaults (e.g. the registry port) in a git-ignored `dev.env` — applies
on the host and in the devcontainer alike. See
[`.devcontainer/README.md`](.devcontainer/README.md).
