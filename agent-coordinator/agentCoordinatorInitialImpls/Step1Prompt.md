# Prompt

[AgentCoordinatorInitial](../AgentCoordinatorInitial.md)

We will create the initial skeleton for agent-coordinator next.

We will take visual cues and technology selection cues from the condoccer.

We will use the same Makefile approaches we have used for the other sub-applications in this project.

The initial UI will be similar to the UI of local-representative at this point, although it will facilitate selection of which host the UIs correspond to, and it will have visibility on the local-representative. At this point the webserver will not do anything other than serve the frontend with websocket (later it will have TCP communication, but that will be deferred). It will use distinct ports from other sub-applications. We will also have some basic starting points for our documentation.

When this increment is complete we will be able to `make build && make run` in our agent-coordinator and have our new sub-application up and running.
