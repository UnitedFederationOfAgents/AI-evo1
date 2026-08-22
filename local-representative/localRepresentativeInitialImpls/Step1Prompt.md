# Prompt

[LocalRepresentativeInitial](../LocalRepresentativeInitial.md)

We will create the initial skeleton for local-representative next.

We will take visual cues and technology selection cues from the condoccer.

We will use the same Makefile approaches we have used for the other sub-applications in this project.

The initial UI will be a large pane with selection tabs on the top for 'federation-command', 'condoccer', and 'worker. Similarly to the condoccer this UI will have a websocket connection for the client. In this iteration the tabs for each of the selections will have a 'healthy' indicator that always displays healthy (faking it for now).

When this increment is complete we will be able to `make build && make run` and have our new sub-application up and running.
