# Prompt

[LocalSessionImprovments](../LocalSessionImprovments.md)

We will implement a feature to archive sessions.

This will be implemented through clauditable, and we will give federation-command a command it may use to invoke this.

This will be the first time we use the 'ufa command tree'. This is a tree of commands which covers all commands native to federation-commands. We'll include a UFA_COMMANDS_NEXT_STEP.md to concisely draft the rest of the tree, covering only commands that already exist.

In the case of our current session-related implementation we will introduce the command 'ufa session archive'. It will come with a decorative confirmation dialog since it is a sensitive command.

The only other command we will introduce is 'help', which will also be the default option if none is selected (ie: 'ufa' == 'ufa help', 'ufa session' == 'ufa session help').

When a federation-command user uses 'ufa session archive' it will move all sessions from the AGENT_RECORDS_PATH to AGENT_RECORDS_ARCHIVE_PATH/<date-time> -- where AGENT_RECORDS_ARCHIVE_PATH defaults to $AGENT_RECORDS_PATH-archive (so for /host-agent-files/agent-records it would be /host-agent-files/agent-records-archive). This will happen via federation-command calling clauditable's entrypoint. Clauditable may also have the entrypoint called directly.

When this increment is complete we will be able to rebuild FC (federation-command) and CLBL (clauditable) and archive our sessions from FC.
