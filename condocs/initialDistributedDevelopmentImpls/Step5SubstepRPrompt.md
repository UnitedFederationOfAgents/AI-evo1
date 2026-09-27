# Prompt

[Step5Prompt](Step5Prompt.md)

We will implement integrated file-inclusion capabilities in condoccer to facilitate a better loop for bringing output data back into the development process.

We will begin by adding a concept to the 'files' capability called 'highlighting'. When we select a file we will be able to 'highlight' the file with a new control in the same menu as 'enter'/'hold'/'download'/etc.

This will mark the file at the level of LR/AC in preparation to be used for cross-system functionality. During this implement we will only implement the 'highlighting' phase. The 'highlight' option will be a toggle and any highlighted file will have a yellow line circling the box.


prompt: [`f92a5ca`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/f92a5ca2be506e89a6234b9b7876844f65b2c85c) → reply: [`cfb2a25`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/cfb2a251a44b1302f790628a1aa38a05e2c52d04)

## Reply

All changes are in place across both apps' Go backends, TypeScript types, React frontends, CSS, and docs, plus new tests. No commit was made — leaving that to the condoc handler as instructed.

## Summary

Implemented Step 5 Substep R: added a "highlighting" toggle to the files capability's existing enter/hold/persist/download menu, in both local-representative and agent-coordinator. A file's `Highlighted` state is a plain, state-independent flag persisted in its `.manifest_<id>.yaml` sidecar (survives hold/persist rewrites and LR restarts), toggled via a new `POST /api/files/<id>/highlight` endpoint (ungated on `proxiedHeader`, same as hold/persist/delete, so it passes through AC's transparent proxy unmodified). The file-details dialog gained a "highlight"/"unhighlight" button, and a highlighted file's grid box now shows a yellow ring (`box-shadow`) in both frontends. I mirrored the new `Highlighted`/`highlighted` field into AC's Go `FileInfo` struct and both apps' TypeScript types (avoiding the Revision K class of silently-dropped-field bug), added Go unit tests in `local-representative/files_test.go` and a JSON round-trip regression test in `agent-coordinator/files_test.go`, and documented the mechanism in `docs/CurrentPersistentFiles.md` and `docs/DistributedExchange.md`. As with all prior revisions, `go build`/`go test` are denied in this environment, so I verified correctness via careful read-through.


## Revision A

Next we will implement both the condoccer mechanism to add a resource into a condoc and also the AC/LR facilitated mechanism to bring highlighted files into the context of other sub-apps that can use them via the SUBAPP<-->LR<-->AC connection.

On the condoccer side the resource-inclusion will work as follows:
- A new button will be available within steps and substeps called 'Add Resources'
- The button will bring up a sub-dialog which for now will only have the additional option 'Highlighted' as well as a description field
- When 'Add Resources'-->'Highlighted' is selected condoccer will bring the highlighted files into the scope of the condoc
  - The files will be copied into the <condoc>Impls folder
  - There will be a section of text inserted into the condoc which links to the resource file
  - The resource-and-description block will be inserted ABOVE the '## <REPLACE-Revision|Retry> A' line, creating a new '## Resouce (N)' block with proper spacing
  - The '.condoc' lock will be asserted here because we do not want to rebuild upon inclusion of condoc resources


prompt: [`048e0a1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/048e0a102d36bfd3bf27cc188a3cc49942471043) → reply: [`9daabef`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/9daabefa6a037bb4b615e5aa784f673a2a9d4e81)

## Reply A

Good — the substep prompt file itself wasn't touched, and `.condoc` remains an untracked artifact from the harness, not something I wrote. All changes are in place.

I implemented Revision A across the stack:

- **`representable` package**: `Server` gained an opt-in `SetHTTPPort`, disclosed to every connecting client via the existing "hello" message; `Client` gained `PeerHTTPPort()` to read it back. **local-representative** now calls `SetHTTPPort` with its own dashboard port, so condoccer (which only dialed LR's representable port) can now reach LR's HTTP file API directly.
- **condoccer backend** (`condoccer/resources.go`, new): a new `add_resource` action pulls every currently-highlighted file from LR's `GET /api/files`/`GET /api/files/<id>` (already ungated, so it works through AC's proxy unmodified too), copies them into the condoc's `<name>Impls` folder, and inserts a `## Resource (N)` block linking to them directly above the pending `## <REPLACE-Revision|Retry>` placeholder — asserting the `.condoc` lock for the whole copy+edit so LR's dev-repo watcher can't rebuild mid-operation.
- **condoccer frontend**: `ActionPanel` (shared by step and substep views) gained an "Add Resources…" button opening a form with the "Highlighted" source option and a description field, sending the new action over the existing WebSocket.
- Added Go tests for the markdown insertion/numbering logic, the HTTP fetch-and-copy path (via `httptest`), and the new `representable` HTTPPort disclosure; updated `docs/DistributedExchange.md` and `docs/CurrentPersistentFiles.md` to describe the mechanism. As before, `go build`/`go test` are denied in this environment, so I verified correctness by careful read-through rather than compiling.


## <REPLACE-Revision|Retry> B

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
