# Prompt

[AgentCoordinatorInitial](../AgentCoordinatorInitial.md)

We will create the initial skeleton for agent-coordinator next.

We will take visual cues and technology selection cues from the condoccer.

We will use the same Makefile approaches we have used for the other sub-applications in this project.

The initial UI will be similar to the UI of local-representative at this point, although it will facilitate selection of which host the UIs correspond to, and it will have visibility on the local-representative. At this point the webserver will not do anything other than serve the frontend with websocket (later it will have TCP communication, but that will be deferred). It will use distinct ports from other sub-applications. We will also have some basic starting points for our documentation.

When this increment is complete we will be able to `make build && make run` in our agent-coordinator and have our new sub-application up and running.


prompt: [`39bb38d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/39bb38d8e5ca15be8c7850438b351e871c614c70) → reply: [`39d33c3`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/39d33c3fe104b5efeeef3fe8206530cc1cc7e9a4)

## Reply

The `agent-coordinator` skeleton is complete. Here is a summary of what was created:

**Go backend** (`main.go`, `go.mod`, `go.sum`, `Makefile`): HTTP server on port 8083 with WebSocket support using the same gorilla/websocket + `go:embed` pattern as local-representative. Tracks a host list (empty at startup), handles `select-host` messages from clients, and broadcasts `hosts`/`lr-state` messages. TCP connections to local-representatives are stubbed out (deferred to a later step).

**React/TypeScript frontend** (`frontend/`): Dark-themed two-panel UI — a host sidebar on the left (with connection-status dots) and a main area on the right that mirrors the local-representative service-tab view for the selected host. Uses the same Vite + WebSocket hook pattern as local-representative, proxying `/ws` to `:8083`.

**Documentation** (`docs/architecture.md`): Records port assignments, the WebSocket message protocol, host states, and what is deferred.

**Top-level Makefile**: `agent-coordinator` added to `SUBPROJECTS`. Running `make build && make run` inside `agent-coordinator/` will build the frontend, embed it into the Go binary, and start the server.


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
