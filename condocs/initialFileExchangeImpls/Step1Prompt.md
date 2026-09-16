# Prompt

[InitialFileExchange](../InitialFileExchange.md)

We will start with the functional stack to support the 'files' tab through local-representative.

This tab will appear beside 'system' and will display an area for simple wireframe file icons. (Just basic types, text, image - a very small set to start)
The tab will be visible through agent-coordinator's view as well as LR's.

When we drag a file from our system's finder (or equivalent) onto this window it will be uploaded and will end up in local-representative's host-cache directory (/host-agent-files/exchange/host-cache by default). For this increment we will not allow this INPUT through agent-coordinator, we will only allow the direct client of local-representative -- but we will also respond with DistributedExchange.md discussing how this might be extended to local-representative<-->agent-coordinator<-->local-representative chains or agent-coordinator<-->local-representative chains.

These files in the host-cache will be deleted after one hour.

When we click on the files in the main pane we will have a side-pane on the right hand side that displays file details.


prompt: [`f834ce8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/f834ce8aa155f8355ee677a439206d5b9f9b6e0e) → reply: [`ffc4168`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ffc41681412510bf72bc1ad5ecb6716c09f6c083)

## Reply

All the intended files were touched, and the condoc files themselves were left untouched. The implementation is complete.

## Summary

Implemented Step 1 of `InitialFileExchange`: a `files` tab (beside `system`) on both local-representative and agent-coordinator. LR gained a host-cache directory (`/host-agent-files/exchange/host-cache` by default, `-file-cache-dir` flag), a `/api/files` upload/list endpoint with a wireframe-icon grid (text/image/other) and drag-and-drop, an hourly TTL sweep, and a click-to-open right-hand detail pane; the state mirrors up to agent-coordinator over `representable` for a read-only view there. Upload-via-AC is refused server-side (not just hidden in the UI) via an `X-UFA-Proxied-By` header AC's reverse proxy stamps and LR's upload handler checks. Wrote `docs/DistributedExchange.md` as requested, discussing how this could extend to AC-mediated and LR↔AC↔LR cross-host transfer, plus added Go tests (`files_test.go`) and updated READMEs/architecture docs.

One limitation to flag: this sandbox blocks `go build`/`go vet`/`npm`/`gofmt` outright (confirmed via a separate subagent too), so none of this could be compiled or run — I relied on careful manual review (brace-balance checks, full re-reads of every diff) instead of a real build/test pass. Worth compiling and running `make test` before merging.


## Revision A

Now let's improve this functionality by adding a viewer page as well as a 'download' widget.

We'll be able to go into this viewer either through a double-click or an 'enter' widget, reminiscent of what we see from entering through condocs.

Let's add a 'download' button to the file details dialog which does the obvious. (Downloads the file)

On the file system where we store the files locally (/host-agent-files/exchange/host-cache/) we should also add files we hide from the 'files' window where we keep details (for a file 'd992a7a9_debug.txt' we might have '.manifest_d992a7a9_debug.txt.yaml'). We will refuse the upload of files starting with .manifest_. This will help us understand when cache entries will be deleted if our application was interrupted, as well as letting us store other details.
