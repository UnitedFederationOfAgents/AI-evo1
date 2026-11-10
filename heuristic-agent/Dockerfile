ARG GO_VERSION=1.25

# Build stage - builds heuristic-agent and all dependencies
FROM golang:${GO_VERSION} AS builder

WORKDIR /build

# Copy all required sub-projects
COPY heuristic-agent/ ./heuristic-agent/
COPY clauditable/ ./clauditable/
COPY ambiguous-agent/ ./ambiguous-agent/
COPY clod/ ./clod/

# Build heuristic-agent
WORKDIR /build/heuristic-agent
RUN go build -o heuristic-agent .

# Build clauditable
WORKDIR /build/clauditable
RUN go build -o clauditable .

# Build ambiguous-agent
WORKDIR /build/ambiguous-agent
RUN go build -o ambiguous-agent .

# Build clod
WORKDIR /build/clod
RUN go build -o clod .

# Runtime stage
FROM golang:${GO_VERSION}

WORKDIR /app

# Copy all built binaries
COPY --from=builder /build/heuristic-agent/heuristic-agent /app/heuristic-agent
COPY --from=builder /build/clauditable/clauditable /app/clauditable
COPY --from=builder /build/ambiguous-agent/ambiguous-agent /app/ambiguous-agent
COPY --from=builder /build/clod/clod /app/clod

# Add to PATH so components can find each other
ENV PATH="/app:${PATH}"

# Default environment variables
ENV SLOPSPACES_DIR="/host-agent-files/slopspaces"
ENV WORK_SIGNALS_DIR="/host-agent-files/work"
ENV AGENT_SLOPSPACE_ROOT="/agent"
ENV AGENT_RECORDS_PATH="/host-agent-files/agent-records"

# Create required directories
RUN mkdir -p /host-agent-files/slopspaces \
    && mkdir -p /host-agent-files/work/ongoing \
    && mkdir -p /host-agent-files/work/complete \
    && mkdir -p /host-agent-files/agent-records \
    && mkdir -p /agent/agent-worker \
    && mkdir -p /agent/heuristic-request

# Default to watch mode
ENTRYPOINT ["/app/heuristic-agent"]
CMD ["watch", "--agent-type", "agent-worker"]
