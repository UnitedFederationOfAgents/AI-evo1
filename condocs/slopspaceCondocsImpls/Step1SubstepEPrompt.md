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


## <REPLACE-Revision|Retry> B

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
