# .devcontainer/Dockerfile
FROM mcr.microsoft.com/devcontainers/go:dev-1.25-bookworm

# Freeze the Go toolchain to ensure absolute determinism matching Go v1.25.0
ENV GOTOOLCHAIN=local

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    git \
    make \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Note: We use '--mount=type=bind' in the RUN commands to mount .tool-versions ephemerally.
# This prevents version definitions from bloating the image layers or leaving residual files in /tmp.
# Dynamic architecture detection via 'dpkg --print-architecture' is used (standard in this Debian-based image).

# 1. Install kubectl and verify official SHA256 checksum
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    KUBECTL_VERSION=$(awk '/^kubectl/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl" \
    && curl -fsSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl.sha256" -o /tmp/kubectl.sha256 \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c - \
    && chmod +x /usr/local/bin/kubectl \
    && rm -f /tmp/kubectl.sha256

# 2. Install Helm and verify official SHA256 checksum
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    HELM_VERSION=$(awk '/^helm/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && cd /tmp \
    && curl -fsSL -O "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz" \
    && curl -fsSL -O "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz.sha256sum" \
    && sha256sum -c "helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz.sha256sum" \
    && tar -xzf "helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz" \
    && mv "linux-${ARCH}/helm" /usr/local/bin/helm \
    && chmod +x /usr/local/bin/helm \
    && rm -rf "helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz" "helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz.sha256sum" "linux-${ARCH}"

# 3. Install Kind and verify official SHA256 checksum
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    KIND_VERSION=$(awk '/^kind/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && cd /tmp \
    && curl -fsSL -O "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${ARCH}" \
    && curl -fsSL -O "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${ARCH}.sha256sum" \
    && sha256sum -c "kind-linux-${ARCH}.sha256sum" \
    && mv "kind-linux-${ARCH}" /usr/local/bin/kind \
    && chmod +x /usr/local/bin/kind \
    && rm -f "kind-linux-${ARCH}.sha256sum"

# 4. Install Flux CLI and verify official SHA256 checksum
# Note: checksums.txt covers all platforms; we extract the line matching our local tarball
# to guarantee a real verification (avoids the silent passthrough trap with --ignore-missing).
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    FLUX_VERSION=$(awk '/^flux/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && cd /tmp \
    && curl -fsSL -O "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/flux_${FLUX_VERSION}_linux_${ARCH}.tar.gz" \
    && curl -fsSL -O "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/flux_${FLUX_VERSION}_checksums.txt" \
    && EXPECTED_SHA=$(grep "  flux_${FLUX_VERSION}_linux_${ARCH}.tar.gz$" "flux_${FLUX_VERSION}_checksums.txt") \
    && [ -n "$EXPECTED_SHA" ] \
    && echo "$EXPECTED_SHA" | sha256sum -c - \
    && tar -xzf "flux_${FLUX_VERSION}_linux_${ARCH}.tar.gz" \
    && mv flux /usr/local/bin/flux \
    && chmod +x /usr/local/bin/flux \
    && rm -rf "flux_${FLUX_VERSION}_linux_${ARCH}.tar.gz" "flux_${FLUX_VERSION}_checksums.txt"

# 5. Install Kustomize and verify official SHA256 checksum
# Note: kustomize release tags are URL-encoded as 'kustomize%2FvX.Y.Z'.
# checksums.txt covers all platforms; we extract the matching line (same pattern as Flux).
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    KUSTOMIZE_VERSION=$(awk '/^kustomize/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && cd /tmp \
    && curl -fsSL -O "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${KUSTOMIZE_VERSION}/kustomize_v${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz" \
    && curl -fsSL -O "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${KUSTOMIZE_VERSION}/checksums.txt" \
    && EXPECTED_SHA=$(grep "  kustomize_v${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz$" checksums.txt) \
    && [ -n "$EXPECTED_SHA" ] \
    && echo "$EXPECTED_SHA" | sha256sum -c - \
    && tar -xzf "kustomize_v${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz" \
    && mv kustomize /usr/local/bin/kustomize \
    && chmod +x /usr/local/bin/kustomize \
    && rm -rf "kustomize_v${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz" checksums.txt

# 6. Install Node.js and verify official SHA256 checksum
# Tarball install (not the base image's nvm) so node/npm/npx land in /usr/local/bin,
# on PATH for every user including root. Required by Prettier (Markdown formatting).
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    NODE_VERSION=$(awk '/^nodejs/ {print $2}' /tmp/.tool-versions) \
    && ARCH=$(dpkg --print-architecture) \
    && case "$ARCH" in amd64) NODE_ARCH=x64 ;; arm64) NODE_ARCH=arm64 ;; *) echo "Unsupported arch: $ARCH" >&2; exit 1 ;; esac \
    && cd /tmp \
    && curl -fsSL -O "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.gz" \
    && curl -fsSL -O "https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt" \
    && EXPECTED_SHA=$(grep "  node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.gz$" SHASUMS256.txt) \
    && [ -n "$EXPECTED_SHA" ] \
    && echo "$EXPECTED_SHA" | sha256sum -c - \
    && tar -xzf "node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.gz" -C /usr/local --strip-components=1 --no-same-owner \
    && rm -f /usr/local/CHANGELOG.md /usr/local/LICENSE /usr/local/README.md \
    && rm -rf "node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.gz" SHASUMS256.txt \
    && node --version && npm --version

# 7. Install Prettier globally (used by the pre-commit Markdown hook)
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    PRETTIER_VERSION=$(awk '/^prettier/ {print $2}' /tmp/.tool-versions) \
    && npm install -g "prettier@${PRETTIER_VERSION}" \
    && npm cache clean --force \
    && prettier --version

# 8. Install pre-commit in an isolated virtualenv (avoids PEP 668 system-package conflicts)
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    PRECOMMIT_VERSION=$(awk '/^pre-commit/ {print $2}' /tmp/.tool-versions) \
    && apt-get update && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/precommit \
    && /opt/precommit/bin/pip install --no-cache-dir "pre-commit==${PRECOMMIT_VERSION}" \
    && ln -sf /opt/precommit/bin/pre-commit /usr/local/bin/pre-commit \
    && pre-commit --version

# 9. Pre-cache envtest assets in /usr/local/share/envtest
# Pinned to a SHA on the release-0.23 branch (Go 1.25-compatible); published
# v0.24.x tags require Go 1.26. To bump:
#   git ls-remote https://github.com/kubernetes-sigs/controller-runtime release-0.23
RUN --mount=type=bind,source=.tool-versions,target=/tmp/.tool-versions \
    K8S_ENVTEST_VERSION=$(awk '/^kubectl/ {split($2, a, "."); print a[1]"."a[2]}' /tmp/.tool-versions) \
    && GOBIN=/usr/local/bin go install sigs.k8s.io/controller-runtime/tools/setup-envtest@f9589b9f2b9dddf8532b432bb8315f2820ab9971 \
    && mkdir -p /usr/local/share/envtest \
    && ENVTEST_PATH=$(/usr/local/bin/setup-envtest use "${K8S_ENVTEST_VERSION}" --bin-dir /usr/local/share/envtest -p path | tail -n 1) \
    && ln -s "${ENVTEST_PATH}" /usr/local/share/envtest/active \
    && chmod -R a+rx /usr/local/share/envtest
