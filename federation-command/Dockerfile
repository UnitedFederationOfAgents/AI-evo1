ARG GO_VERSION=1.25

# Build stage
FROM golang:${GO_VERSION} AS builder

WORKDIR /app

# Copy federation-command source
COPY . .

RUN make build

# Build clauditable from sibling project
WORKDIR /clauditable
COPY ../clauditable/ .
RUN go build -o clauditable .

# Runtime stage
FROM golang:${GO_VERSION}

WORKDIR /app

# Copy built binaries
COPY --from=builder /app/federation-command /app/federation-command
COPY --from=builder /clauditable/clauditable /app/clauditable

# Add to PATH so federation-command can find clauditable
ENV PATH="/app:${PATH}"

# Install ambiguous-agent (must be available in PATH or built separately)
# For production, ambiguous-agent should be installed via package manager or copied here

ENTRYPOINT ["/app/federation-command"]
