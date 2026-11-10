# AgentCoordinatorInitial

<!--
```condoc-yaml
condoc:
  startTime: 1788016968
  controlScheme: same-repo
  branch: condoc/AgentCoordinatorInitial-1788016968/main
  callerPath: ..
```
-->

Implement the agent-coordinator application — a hierarchically organized network coordination, conventionally organized into a 'net coordinator' and 'web coordinator' layout. Interacts with local-representatives to pass UI through to frontend users and make distributed systems available to each other.


### Step 1 - Create Initial Runtime and Intergration

[Step 1 Prompt](agentCoordinatorInitialImpls/Step1Prompt.md)

```prompt
We will create the initial skeleton for agent-coordinator next.

We will take visual cues and technology selection cues from the condoccer.

We will use the same Makefile approaches we have used for the other sub-applications in this project.

The initial UI will be similar to the UI of local-representative at this point, although it will facilitate selection of which host the UIs correspond to, and it will have visibility on the local-representative. At this point the webserver will not do anything other than serve the frontend with websocket (later it will have TCP communication, but that will be deferred). It will use distinct ports from other sub-applications. We will also have some basic starting points for our documentation.

When this increment is complete we will be able to `make build && make run` in our agent-coordinator and have our new sub-application up and running.
```


## Condoc Completed

This condoc was completed at 1788268451 (Tue Sep 1 01:14:11 PM UTC 2026).
