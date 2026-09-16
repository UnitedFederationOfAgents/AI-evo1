# Prompt

[InitialFileExchange](../InitialFileExchange.md)

We will start with the functional stack to support the 'files' tab through local-representative.

This tab will appear beside 'system' and will display an area for simple wireframe file icons. (Just basic types, text, image - a very small set to start)
The tab will be visible through agent-coordinator's view as well as LR's.

When we drag a file from our system's finder (or equivalent) onto this window it will be uploaded and will end up in local-representative's host-cache directory (/host-agent-files/exchange/host-cache by default). For this increment we will not allow this INPUT through agent-coordinator, we will only allow the direct client of local-representative -- but we will also respond with DistributedExchange.md discussing how this might be extended to local-representative<-->agent-coordinator<-->local-representative chains or agent-coordinator<-->local-representative chains.

These files in the host-cache will be deleted after one hour.

When we click on the files in the main pane we will have a side-pane on the right hand side that displays file details.
