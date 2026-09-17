# InitialFileExchange

<!--
```condoc-yaml
condoc:
  startTime: 1789584310
  controlScheme: same-repo
  branch: condoc/InitialFileExchange-1789584310/main
  callerPath: ..
```
-->

Add baseline file exchange features in preparation for distributed session support.


### Step 1 - Begin with the host-exchange basics for file handling.

[Step 1 Prompt](initialFileExchangeImpls/Step1Prompt.md)

```prompt
We will start with the functional stack to support the 'files' tab through local-representative.

This tab will appear beside 'system' and will display an area for simple wireframe file icons. (Just basic types, text, image - a very small set to start)
The tab will be visible through agent-coordinator's view as well as LR's.

When we drag a file from our system's finder (or equivalent) onto this window it will be uploaded and will end up in local-representative's host-cache directory (/host-agent-files/exchange/host-cache by default). For this increment we will not allow this INPUT through agent-coordinator, we will only allow the direct client of local-representative -- but we will also respond with DistributedExchange.md discussing how this might be extended to local-representative<-->agent-coordinator<-->local-representative chains or agent-coordinator<-->local-representative chains.

These files in the host-cache will be deleted after one hour.

When we click on the files in the main pane we will have a side-pane on the right hand side that displays file details.
```


### Step 2 - Take a brief detour to improve the mobile UI.

[Step 2 Prompt](initialFileExchangeImpls/Step2Prompt.md)

```prompt
Currently our UIs are all either very ugly or completely inoperable through a mobile interface.

In as decoupled of a way as possible, let's properly serve a mobile web browser with an appropriate presentation. Let's start with condoccer and not yet worry about local-representative or agent-coordinator.

Let's keep things concise at first and start with something straightforward and simple. We'll pivot from there as-needed.
```


### Step 3 - Add persistence controls.

[Step 3 Prompt](initialFileExchangeImpls/Step3Prompt.md)

```prompt
Let's add the following controls to the file details dialog:
- delete: has a confirm dialog, deletes the file
- hold/persist: the button originally appears as 'hold', when pressed the file's manifest is updated to mark the file for 72 hour cache. If a file is held then the button 'persist' appears in place of hold. Pressing 'persist' moves the file to the host-store (/host-agent-files/exchange/host-store by default) where it will not be automatically deleted.

The file icons should be updates to wireframe icons - when a file is cached short-term it appears as orange. When a file is held it is yellow. When a file is persisted it turns green.
```


## Condoc Completed

This condoc was completed at 1789671512 (Thu Sep 17 06:58:32 PM UTC 2026).
