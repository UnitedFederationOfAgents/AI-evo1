# syntax=docker/dockerfile:1

# https://github.com/helmfile/helmfile/pkgs/container/helmfile
ARG HELMFILE_VERSION=v0.151.0

# https://hub.docker.com/r/alpine/helm/tags
ARG HELM_VERSION=3.12.2

# https://hub.docker.com/r/hadolint/hadolint/tags
ARG HADOLINT_VERSION=v2.12.0

# https://hub.docker.com/r/koalaman/shellcheck/tags
ARG SHELLCHECK_VERSION=v0.9.0

# https://hub.docker.com/r/alpine/terragrunt/tags

ARG TERRAGRUNT_VERSION=1.13.2

# https://github.com/terraform-linters/tflint/pkgs/container/tflint
ARG TFLINT_VERSION=v0.48.0

# https://github.com/terraform-docs/terraform-docs/releases
ARG TERRAFORM_DOCS_VERSION=0.16.0

# https://github.com/aquasecurity/trivy/releases
ARG TRIVY_VERSION=0.49.1

# https://hub.docker.com/r/mikefarah/yq/tags
ARG YQ_VERSION=4.40.7

FROM ghcr.io/helmfile/helmfile:${HELMFILE_VERSION} as helmfile
FROM alpine/helm:${HELM_VERSION} as helm
FROM ghcr.io/hadolint/hadolint:${HADOLINT_VERSION} as hadolint
FROM koalaman/shellcheck:${SHELLCHECK_VERSION} as shellcheck
FROM alpine/terragrunt:${TERRAGRUNT_VERSION} as terragrunt
FROM ghcr.io/terraform-linters/tflint:${TFLINT_VERSION} as tflint
FROM quay.io/terraform-docs/terraform-docs:${TERRAFORM_DOCS_VERSION} as terraform-docs
FROM aquasec/trivy:${TRIVY_VERSION} as trivy
FROM mikefarah/yq:${YQ_VERSION} as yq

FROM mcr.microsoft.com/devcontainers/go:1.25-bookworm

COPY --from=helmfile /usr/local/bin/helmfile /usr/local/bin/
COPY --from=helm /usr/bin/helm /usr/local/bin/
COPY --from=hadolint /bin/hadolint /usr/local/bin/
COPY --from=shellcheck /bin/shellcheck /usr/local/bin/
COPY --from=terragrunt /bin/terraform /usr/local/bin/terragrunt /usr/local/bin/
COPY --from=tflint /usr/local/bin/tflint /usr/local/bin/
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/
COPY --from=terraform-docs /usr/local/bin/terraform-docs /usr/local/bin/
COPY --from=yq /usr/bin/yq /usr/local/bin/

COPY rootfs /

COPY install_devcontainer_utilities.sh /tmp/install_devcontainer_utilities.sh

# Install additional utilities in the devcontainer directly.
RUN bash /tmp/install_devcontainer_utilities.sh

HEALTHCHECK CMD echo healthy

USER $USERNAME
