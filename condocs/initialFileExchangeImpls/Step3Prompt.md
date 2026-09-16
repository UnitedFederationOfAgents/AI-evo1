# Prompt

[InitialFileExchange](../InitialFileExchange.md)

Let's add the following controls to the file details dialog:
- delete: has a confirm dialog, deletes the file
- hold/persist: the button originally appears as 'hold', when pressed the file's manifest is updated to mark the file for 72 hour cache. If a file is held then the button 'persist' appears in place of hold. Pressing 'persist' moves the file to the host-store (/host-agent-files/exchange/host-store by default) where it will not be automatically deleted.

The file icons should be updates to wireframe icons - when a file is cached short-term it appears as orange. When a file is held it is yellow. When a file is persisted it turns green.


prompt: [`bb6d8fc`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/bb6d8fc8697888564a9423d98703a3b20cf453fb) → reply: [`8208ea1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8208ea19bb9bf195c14f42f2bbb4e086f6de20f1)

## Reply

Implemented the file-details dialog's delete/hold/persist controls end-to-end: local-representative's backend (`files.go`, `main.go`) gained a `state` field (cached/held/persisted), a new `-host-store-dir` flag, and `DELETE`/`POST .../hold`/`POST .../persist` routes with manifest-backed hold expiry (72h) and sweep logic that respects it, all covered by new Go tests. Both local-representative's and agent-coordinator's React frontends got matching wireframe SVG icons (replacing the old emoji set) colored orange/yellow/green by cache state, plus hold/persist and delete (with a confirm dialog) buttons in the file-details pane, wired straight through AC's existing transparent proxy with no new relay route needed. Docs (`CurrentPersistentFiles.md`, `DistributedExchange.md`, LR's `README.md`) were updated to match.

One caveat: this session's permission mode rejected every build/compile/typecheck command I attempted (`go build`, `go vet`, `gofmt`, `npx tsc`, `npm run build`), so I could not run automated verification — I instead did a careful manual read-through of the full diff for compile-correctness and logical consistency, but the code has not been build- or test-verified in this session.


## Revision A

This functionality for hold/persist is a good start but has some issues:
- When we hold the timer shows a large number of minutes remaining, but it should now show hours and minutes.
- When we 'hold' the button does not change to 'persist' and the icon does not change to yellow *in the agent-coordinator UI specifically* - it works in local-representative

Let's fix these items.
