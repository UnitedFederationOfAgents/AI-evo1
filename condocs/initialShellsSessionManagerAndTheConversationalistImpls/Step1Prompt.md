# Prompt

[InitialShellsSessionManagerAndTheConversationalist](../InitialShellsSessionManagerAndTheConversationalist.md)

We will begin by creating the application shells for Session Manager (SM) and The Conversationalist (TC).

Both of these sub-apps will be go-backend-with-UI-frontend just like condoccer.

They will follow the same structure of standalone-UI-with-distinct-port/embed-inlocal-representative/embed-in-agent-coordinator.
They will be included in 'make deploy-dev-binaries'.
They will be launchable in the per-host system tab and visible in the global system tab.
They will have auto-connect and dev-mode configurability.
They will each have their own tab between 'condoccer' and 'worker' -- called 'convo' and 'sessions'.
