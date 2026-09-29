# InitialShellsSessionManagerAndTheConversationalist

<!--
```condoc-yaml
condoc:
  startTime: 1790600862
  controlScheme: same-repo
  branch: condoc/InitialShellsSessionManagerAndTheConversationalist-1790600862/main
  callerPath: ..
```
-->

Create the applications and minimal baseline functionality for Session Manager (SM) and The Conversationalist (TC).


### Step 1 - We will begin by creating the application shells for Session Manager and The Conversationalist.

[Step 1 Prompt](initialShellsSessionManagerAndTheConversationalistImpls/Step1Prompt.md)

```prompt
We will begin by creating the application shells for Session Manager (SM) and The Conversationalist (TC).

Both of these sub-apps will be go-backend-with-UI-frontend just like condoccer.

They will follow the same structure of standalone-UI-with-distinct-port/embed-inlocal-representative/embed-in-agent-coordinator.
They will be included in 'make deploy-dev-binaries'.
They will be launchable in the per-host system tab and visible in the global system tab.
They will have auto-connect and dev-mode configurability.
They will each have their own tab between 'condoccer' and 'worker' -- called 'convo' and 'sessions'.
```


### Step 2 - Add some cross-app integration with TC.

[Step 2 Prompt](initialShellsSessionManagerAndTheConversationalistImpls/Step2Prompt.md)

```prompt
Now let's add a piece of cross-app integration to allow voice input into condoccer input fields.

Condoccer will use the CO<-->LR<-->AC chain to poll for availability of TC. When one or more TC instance is available (on any host) it will recognize TC as available.

We will show when we have TC availability with a mic icon beside the camera icon in our AGENT-COORDINATOR bar. (Illuminated when available)

Whenever we have a text input field for condoccer and it sees TC is available we will see a 'mic' icon become visible. Pressing this icon will trigger the conversationalist's ability to transcribe text and will populate it into the text field.

We will keep the TC functionality as contained in that sub-app as we can, we only want the text to get into other text boxes.
```


## Human-Prompt

The flow of the condoc is now within the second step.
