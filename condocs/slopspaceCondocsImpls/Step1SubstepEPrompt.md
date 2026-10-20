# Prompt

[Step1Prompt](Step1Prompt.md)

We will support the ability to connect some providers dynamically.

The providers dialog will now gain a 'connect' control.

Of the existing providers we will support only GitHub (only through PATs so far).


prompt: [`8aad2f7`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8aad2f77a5383e952fb5278f86470ab2b2e9a6bc) → reply: [`ef7fad5`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ef7fad5519d458dae11d64f655b99a4a4e3abd49)

## Reply

I added a "connect" control to the providers section, in both AC's per-host system tab and LR's own dashboard. Only GitHub is supported, by PAT. None of it has been built or run: `go build`, `go test` and `git status` all needed approval that wasn't given, so I've only checked it by reading. The changes are uncommitted.

- **Connect:** the button opens a provider picker (only "GitHub (PAT)") and a masked token field. It refuses an empty token or one containing spaces or line breaks. From AC, the token travels to the host's LR as a `__system:connect github <token>` command; on LR's own dashboard it goes straight to LR.
- **Where it's kept:** LR saves the PAT to `~/.ufa/connections/github-pat` (readable only by you), so it survives restarts, and re-checks the token straight away. The token is never logged; I also changed the "unrecognised command" log line so it shows only the command name.
- **What uses it:** a connected PAT overrides any PAT in the host's environment and shows "credentials from: connected". Every app LR launches afterwards gets it as `TF_VAR_github_pat`, including condoccer and the dungeon-keeper worker. Apps that are already running keep their old token until they're restarted.
- **Disconnect:** a provider connected this way gets a "disconnect" button, which deletes the saved PAT so LR falls back to the environment's token.

Decisions for you:
- **Plaintext token:** the PAT crosses the AC→LR connection unencrypted unless that link is tunnelled.
- **No error in the UI:** LR saves the token without checking it first. A bad token shows up as GitHub "not working" with the yellow border, and you can disconnect it. A failure to save the token only appears in LR's log, not in the UI.

The backend changes are in `local-representative/connectivity.go` (plus small additions to its `procman.go` and `main.go`) and `agent-coordinator/main.go`. The frontend changes are in both apps' `App.tsx` and `index.css`. A new test, `TestConnectedGitHubPAT` in `local-representative/connectivity_test.go`, covers connect, restart, override and disconnect, but hasn't been run.


## Revision A

Let's adjust it so that we do not save the plaintext token to disk. It's fine that it's ephemeral and will not survive a restart.


prompt: [`0a4568d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0a4568de660c698d22f3c04201079abc95a7d89e) → reply: [`5b5c175`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/5b5c17582dd7c7ed8c23e60c720c7ea1e2f58f26)

## Reply A

I changed it so a connected GitHub PAT is kept only in LR's memory and never written to disk, so it's gone when LR restarts. I removed the code in `local-representative/connectivity.go` that saved, reloaded and deleted `~/.ufa/connections/github-pat`; "disconnect" now just clears the in-memory token, and the imports that only that code used are gone. `TestConnectedGitHubPAT` now checks that a restarted LR does not have the token. Nothing has been built or tested yet, because `go test` needed approval that wasn't given, and none of the edits are committed.


## Revision B

Now let's create a new section below 'providers' called agents & models in the per-host system tab for AC and LR UIs.

This section will be a similar idea to 'providers' but it will specifically focus on agents and models available. It will perform inspection through the ambiguous-agent layer (similarly to how FC will).

After this interaction we will expect to see that we have claude available (and what models).


prompt: [`0a72ba1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0a72ba12e2bb7fcc368585901306f4c68178770e) → reply: [`8599f96`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8599f96150a218c03d75629e12a2695922348e20)

## Reply B

I added an "agents & models" section below "providers" in the system tab of both AC and LR, but none of it has been built or run. `go build`, `go test`, `gofmt` and `git status` all needed approval that wasn't given, so I've only checked it by reading. The changes are uncommitted.

The data comes from a new `--inspect` flag on ambiguous-agent that prints JSON (`ambiguous-agent/inspect.go`): for each agent, whether it's installed, its path, its version (from `--version`) and its models. LR runs it when it starts, every 10 minutes and when you press "re-inspect", and sends the result with the rest of the system-tab data (`local-representative/agents.go`). AC passes it through and handles "re-inspect" for each host. Each installed agent gets a card with its version, path and models; the agents that aren't installed are listed on one line. FC can use the same `--inspect` output later.

Decisions for you:
- **Claude's model list is old:** ambiguous-agent has no way to ask the `claude` CLI for its models, so it reports a fixed list in its code: `opus`, `sonnet`, `haiku` and three dated IDs from the Claude 4 era. Once you have this working you'll see Claude listed, but with those models, not the current ones, until that list is updated.
- **Not recorded:** LR runs the inspection directly, without wrapping it in clauditable the way FC does for `--list-models`, so these regular checks don't fill the agent records with noise.

New tests, `TestInspect` and `TestAgentsWatch`, cover the inspection itself and LR's handling of its results, but haven't been run.


## Revision C

We need to fix our build error:


> ianar-frontend@1.0.0 build
> tsc && vite build

The CJS build of Vite's Node API is deprecated. See https://vite.dev/guide/troubleshooting.html#vite-cjs-node-api-deprecated for more details.
vite v5.4.21 building for production...
transforming...
✓ 34 modules transformed.
rendering chunks...
computing gzip size...
dist/index.html                   0.40 kB │ gzip:  0.27 kB
dist/assets/index-NtcRZ7bl.css   14.20 kB │ gzip:  3.38 kB
dist/assets/index-D9YWzteL.js   185.79 kB │ gzip: 58.40 kB
✓ built in 24.79s
CGO_ENABLED=1 go build -ldflags "-X ufa-version.Version=v0.0.3-slocon-be5d978" -o ianar .
cp ianar /AI-evo1-dev/bin.new/ianar
ianar deployed to /AI-evo1-dev/bin.new
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ianar'

=== Building local-representative ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/local-representative'
cd frontend && npm install && npm run build

up to date, audited 69 packages in 1s

7 packages are looking for funding
  run `npm fund` for details

3 vulnerabilities (1 moderate, 2 high)

To address issues that do not require attention, run:
  npm audit fix

To address all issues (including breaking changes), run:
  npm audit fix --force

Run `npm audit` for details.

> local-representative-frontend@1.0.0 build
> tsc && vite build

src/App.tsx(234,9): error TS6133: 'inspectAgents' is declared but its value is never read.
src/App.tsx(3192,36): error TS2304: Cannot find name 'inspectAgents'.
make[1]: *** [Makefile:17: build-frontend] Error 2
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/local-representative'

=== Build failed -- leaving /AI-evo1-dev/bin untouched ===
make: *** [Makefile:86: deploy-dev-binaries] Error 1




And we also need to add an entrypoint in FC 'ufa agent list' as well as 'ufa agent model list' to use these entrypoints.


prompt: [`fb6fb48`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/fb6fb48b225a4e3fbc507254580d8d86d13737ca) → reply: [`bacdd6d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/bacdd6dd029208627138d1caa9d4150f91b51907)

## Reply C

I fixed the build error and added `ufa agent list` and `ufa agent model list [<agent>]` to FC, but none of it has been built or run. `tsc`, `go test` and `sed` all needed approval that wasn't given, so I've only checked it by reading. The changes are uncommitted.

- **Build error:** LR's frontend defined `inspectAgents` but never passed it out to the place that used it. AC's frontend had the same gap with `sendLRInspectAgents`, which would have been the next build failure; I fixed both (in each app's `frontend/src/App.tsx`).
- **FC commands:** both run `ambiguous-agent --inspect` in the background so the shell doesn't freeze while it checks each agent. `ufa agent list` shows every agent with whether it's installed, its version and path, and marks the current and default agent. `ufa agent model list` shows that agent's models, defaulting to the current agent, and marks the default model and the one you've selected. They're also in the `ufa help` text and in `federation-command/README.md`.

The new code is in `federation-command/agents.go`, wired into `federation-command/main.go`. A new test, `TestRenderAgentInspectDone` in `federation-command/agents_test.go`, covers the output of both commands but hasn't been run.


## <REPLACE-Revision|Retry> D

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
