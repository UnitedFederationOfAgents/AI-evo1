# LocalSessionImprovments

<!--
```condoc-yaml
condoc:
  startTime: 1788618198
  controlScheme: same-repo
  branch: condoc/LocalSessionImprovments-1788618198/main
  callerPath: ..
```
-->

Update local session behaviours in preparation for distributed session support.


### Step 1 - Add ability to archive records and archive records in preparation to implement DistributedSessionsBrainstorm.md

[Step 1 Prompt](localSessionImprovmentsImpls/Step1Prompt.md)

```prompt
We will implement a feature to archive sessions.

This will be implemented through clauditable, and we will give federation-command a command it may use to invoke this.

This will be the first time we use the 'ufa command tree'. This is a tree of commands which covers all commands native to federation-commands. We'll include a UFA_COMMANDS_NEXT_STEP.md to concisely draft the rest of the tree, covering only commands that already exist.

In the case of our current session-related implementation we will introduce the command 'ufa session archive'. It will come with a decorative confirmation dialog since it is a sensitive command.

The only other command we will introduce is 'help', which will also be the default option if none is selected (ie: 'ufa' == 'ufa help', 'ufa session' == 'ufa session help').

When a federation-command user uses 'ufa session archive' it will move all sessions from the AGENT_RECORDS_PATH to AGENT_RECORDS_ARCHIVE_PATH/<date-time> -- where AGENT_RECORDS_ARCHIVE_PATH defaults to $AGENT_RECORDS_PATH-archive (so for /host-agent-files/agent-records it would be /host-agent-files/agent-records-archive). This will happen via federation-command calling clauditable's entrypoint. Clauditable may also have the entrypoint called directly.

When this increment is complete we will be able to rebuild FC (federation-command) and CLBL (clauditable) and archive our sessions from FC.
```


### Step 2 - Update local behaviour with identifiers.

[Step 2 Prompt](localSessionImprovmentsImpls/Step2Prompt.md)

```prompt
We will begin this step by adding host identification.

Many sub-applications need awareness of the "ufa host". Currently all applications with host awareness go straight to "hostname" as a means to self-identify. This implementation will make it so that when a ~/.ufa/host.yaml is not present it will fall through to "hostname" and add the identifier to the host.yaml with the form "<hostname>-<4-char-random-alphanumeric>". If that file is not accessible for whatever reason the "hostname" can still be the fallthrough.

The host.yaml will include a "first configured" timestamp.

We will add the "host" as an attribute to the "session.jsonl" schema for ufa-sessions.
```


### Step 3 - Add initial session leader behaviour.

[Step 3 Prompt](localSessionImprovmentsImpls/Step3Prompt.md)

```prompt
We can see our point-form decisions for our first session leader brainstorm in <repo-root>/condocs/localSessionImprovmentsImpls/SessionLeaderBrainstorm.md.

We want to respect these and to implement our first increment of functionality - we will make the shift from our current clauditable (CLBL) behaviour to our "writing file"/"written file" type behaviour.

In this increment we will implement strictly that functionality, making the most minimal changes possible to complete the feature's initial functionality.

We will make it so that:
- When a call starts (whether it be agent or standard CLI) clauditable checks for any "writing files" and decides if it is primary based on presence of another primary writing file.
- When a call starts (whether it be agent or standard CLI) CLBL emits the "writing file" (format: 1789497089-writing.txt or 1789497089-s-writing.txt)
  - CLBL waits 200ms to check if there was a concurrent command that happened
    - The timestamp on the concurrent command tells us whether there was a collision
    - We switch to secondary if there was a collision
- When a call finishes CLBL creates the "written file" (starting with raw - like: 1789497089-raw.txt or 1789497089-s-raw.txt) and removes the "writing file". The "written file" is similar to what we have today.
  - A written file for a secondary will also include a "-s-", a primary will later clean this up and add it to the session.jsonl.
- We should be able to keep it in one CLBL invocation, with the file writes happening at dispatch time and completion time.
- When a primary completes it cleans up any secondary written files and adds them to the jsonl as well as adding its own written file

- We won't consider any auto-maintenance in this increment, we will implement that later. Written files stay raw for now.
- We won't directly consider any distributed behaviour (across different hosts) yet.

When this increment is complete the session behaviour will feel similar, but the mechanism to deconflict multiple writers will be clean and the foundations will be set for further concurrent operation management.

Let's make these changes now.
```


## Human-Prompt

The flow of the condoc is now within the third step.
