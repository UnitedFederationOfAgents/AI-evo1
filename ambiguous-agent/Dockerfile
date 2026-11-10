ARG GO_VERSION=1.25

FROM golang:${GO_VERSION} AS builder

WORKDIR /app

COPY . .

RUN make build

# Runtime stage - include clauditable as runtime dependency
FROM golang:${GO_VERSION}

WORKDIR /app

# Copy built binaries
COPY --from=builder /app/ambiguous-agent /app/ambiguous-agent

# Copy clauditable from sibling project (must be built first)
# In CI/production, clauditable should be available in PATH or copied here
COPY --from=builder /app/ambiguous-agent /usr/local/bin/ambiguous-agent

# Note: clauditable must be installed separately or included in the base image
# For local development, ensure clauditable is in PATH

ENTRYPOINT ["/app/ambiguous-agent"]
