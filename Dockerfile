ARG GO_VERSION=1.25

# Build stage - builds all sub-projects
FROM golang:${GO_VERSION} AS builder

WORKDIR /build

# Copy all sub-projects
COPY clod/ ./clod/
COPY clauditable/ ./clauditable/
COPY ambiguous-agent/ ./ambiguous-agent/
COPY federation-command/ ./federation-command/

# Build clod
WORKDIR /build/clod
RUN go build -o clod .

# Build clauditable
WORKDIR /build/clauditable
RUN go build -o clauditable .

# Build ambiguous-agent
WORKDIR /build/ambiguous-agent
RUN go build -o ambiguous-agent .

# Build federation-command
WORKDIR /build/federation-command
RUN go build -o federation-command .

# Runtime stage
FROM golang:${GO_VERSION}

WORKDIR /app

# Copy all built binaries
COPY --from=builder /build/clod/clod /app/clod
COPY --from=builder /build/clauditable/clauditable /app/clauditable
COPY --from=builder /build/ambiguous-agent/ambiguous-agent /app/ambiguous-agent
COPY --from=builder /build/federation-command/federation-command /app/federation-command

# Add to PATH so components can find each other
ENV PATH="/app:${PATH}"

# Default agent records path (can be mounted)
ENV AGENT_RECORDS_PATH="/host-agent-files/agent-records"

# Create records directory
RUN mkdir -p /host-agent-files/agent-records

ENTRYPOINT ["/app/federation-command"]
