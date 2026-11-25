# Build the manager binary.
#
# Builder base: registry.access.redhat.com/ubi9/go-toolset, pinned to the newest 1.26 build
# (Go 1.26.10, rebuilt 2026-11-09), multi-arch (amd64/arm64/ppc64le/s390x). Looked up
# 2026-11-25 via:
#   https://catalog.redhat.com/api/containers/v1/repositories/registry/registry.access.redhat.com/repository/ubi9/go-toolset/images?page_size=25&sort_by=creation_date%5Bdesc%5D
FROM registry.access.redhat.com/ubi9/go-toolset:1.26.10-1794208260 AS builder
ARG TARGETOS
ARG TARGETARCH
# go.mod names a newer toolchain for developer and CI builds. The image keeps Red Hat's
# bundled toolchain instead of downloading one mid-build, which certification expects.
ENV GOTOOLCHAIN=local

# The go-toolset image runs as a non-root user by default; switch to root just to get a
# predictable, writable build workspace (this stage's filesystem is discarded).
USER 0
WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -a -o manager cmd/main.go

# Runtime base: registry.access.redhat.com/ubi9/ubi-minimal, pinned to the 9.9 build current
# as of 2026-11-25 (distroless cannot be Red Hat certified; ubi-minimal is the smallest
# certifiable base). Looked up via:
#   https://catalog.redhat.com/api/containers/v1/repositories/registry/registry.access.redhat.com/repository/ubi9/ubi-minimal/images?page_size=10&sort_by=creation_date%5Bdesc%5D
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.9-1795198179

ARG VERSION=0.1.0
ARG RELEASE=1

# Red Hat / OperatorHub container certification labels.
LABEL name="uddi-operator" \
      maintainer="Ingmar <32110797+IngmarVG-IB@users.noreply.github.com>" \
      vendor="uddi-operator authors" \
      version="${VERSION}" \
      release="${RELEASE}" \
      summary="OpenShift operator that shims OpenShift to Infoblox Universal DDI." \
      description="Reconciles uddi.infoblox.com custom resources (DNS views/zones/records, IPAM spaces/blocks/subnets/ranges/addresses, DHCP fixed addresses) against an Infoblox Universal DDI tenant, using per-namespace Connection credentials, ownership tagging and a configurable per-object deletion policy." \
      io.k8s.display-name="Universal DDI Operator" \
      io.openshift.tags="infoblox,dns,ipam,dhcp,ddi,universal-ddi,operator"

COPY --from=builder /workspace/manager /manager
COPY LICENSE /licenses/LICENSE

# OpenShift runs containers as an arbitrary, randomly-assigned UID with the root (0) group;
# keep the binary and license directory group-writable/readable so that model works even
# though this operator does not write to disk at runtime.
RUN chgrp -R 0 /workspace /licenses && \
    chmod -R g=u /workspace /licenses

USER 65532:65532

ENTRYPOINT ["/manager"]
