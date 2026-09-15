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


### Step 3 - <REPLACE-TITLE>

```prompt
<REPLACE-PROMPT>
```


## Human-Prompt

Add the Title and Prompt then submit the '!HANDOFF!' directive to execute the third step, or submit the '!COMPLETED!' directive to complete this condoc.
