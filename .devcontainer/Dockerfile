# .devcontainer/Dockerfile
FROM mcr.microsoft.com/devcontainers/go:dev-1.25-bookworm

# Copy tool-versions file to use as the single source of truth for tool versions
COPY .tool-versions /tmp/.tool-versions

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    git \
    make \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install kubectl
RUN KUBECTL_VERSION=$(awk '/^kubectl/ {print $2}' /tmp/.tool-versions) \
    && curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/$(dpkg --print-architecture)/kubectl" \
    && chmod +x /usr/local/bin/kubectl

# Install Helm
RUN HELM_VERSION=$(awk '/^helm/ {print $2}' /tmp/.tool-versions) \
    && curl -fsSL -o /tmp/helm.tar.gz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-$(dpkg --print-architecture).tar.gz" \
    && tar -xzf /tmp/helm.tar.gz -C /tmp \
    && mv "/tmp/linux-$(dpkg --print-architecture)/helm" /usr/local/bin/helm \
    && chmod +x /usr/local/bin/helm \
    && rm -rf /tmp/helm.tar.gz /tmp/linux-*

# Install Kind
RUN KIND_VERSION=$(awk '/^kind/ {print $2}' /tmp/.tool-versions) \
    && curl -Lo /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/v${KIND_VERSION}/kind-linux-$(dpkg --print-architecture)" \
    && chmod +x /usr/local/bin/kind

# Install Flux CLI
RUN FLUX_VERSION=$(awk '/^flux/ {print $2}' /tmp/.tool-versions) \
    && curl -s https://fluxcd.io/install.sh | bash -s -- "${FLUX_VERSION}"

# Pre-cache envtest assets in /usr/local/share/envtest
# Note: we use release-0.22 branch of setup-envtest which is fully compatible with Go 1.25
RUN K8S_ENVTEST_VERSION=$(awk '/^kubectl/ {split($2, a, "."); print a[1]"."a[2]}' /tmp/.tool-versions) \
    && GOBIN=/usr/local/bin go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22 \
    && mkdir -p /usr/local/share/envtest \
    && /usr/local/bin/setup-envtest use "${K8S_ENVTEST_VERSION}" --bin-dir /usr/local/share/envtest \
    && ln -s /usr/local/share/envtest/k8s/* /usr/local/share/envtest/active \
    && chmod -R a+rx /usr/local/share/envtest

# Ensure Go cache directories are fully owned and writable by the vscode user
RUN mkdir -p /go/pkg /home/vscode/go/pkg \
    && chown -R vscode:vscode /go /home/vscode/go
