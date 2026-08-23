# LocalRepresentativeInitial

<!--
```condoc-yaml
condoc:
  startTime: 1787417969
  controlScheme: same-repo
  branch: condoc/LocalRepresentativeInitial-1787417969/main
  callerPath: ..
```
-->

Implement the local-representative application — a persistent local agent that registers with the federation and handles coordination of all the runtimes on a UFA host.


### Step 1 - Create Initial Runtime and Intergration

[Step 1 Prompt](localRepresentativeInitialImpls/Step1Prompt.md)

```prompt
We will create the initial skeleton for local-representative next.

We will take visual cues and technology selection cues from the condoccer.

We will use the same Makefile approaches we have used for the other sub-applications in this project.

The initial UI will be a large pane with selection tabs on the top for 'federation-command', 'condoccer', and 'worker. Similarly to the condoccer this UI will have a websocket connection for the client. In this iteration the tabs for each of the selections will have a 'healthy' indicator that always displays healthy (faking it for now).

When this increment is complete we will be able to `make build && make run` and have our new sub-application up and running.
```


### Step 2 - Integrate secondary modes of federation-command with local-representative.

[Step 2 Prompt](localRepresentativeInitialImpls/Step2Prompt.md)

```prompt
During this step we will first look at our options for implementing integration between federation-command functions like ridealongs and condoc mode.

We want to have as similar of an experience as possible on the web UI compared to the terminal UI. The user maintains the same paradigm of selecting between local and remote control, and both consoles are able to see the selectable option and which option the other console selects as they select it (when permitted due to local/remote control).

First, in a pure-response capacity with no code updates, we will describe three paths to implementation with distinct levels of effort/comprehensiveness.
```


## Human-Prompt

The flow of the condoc is now within the second step.
