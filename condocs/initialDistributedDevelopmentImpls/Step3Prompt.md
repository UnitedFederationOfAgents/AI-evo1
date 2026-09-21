# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Now that we have restart mechanisms available we need the ability to detect changes and automatically rebuild.

We will start by giving this ability to LR, specifically in dev-mode. We can now launch LR with the argument --dev-repo: this automatically sets dev-mode and also marks the current working directory repo as watched. (We must be in a repo for this to be accepted)

When the repo is watched LR will watch the HEAD for changes. If there are changes detected there is an indication in the system tab (a 'rebuild' button becomes available). If the repo is dirty (modified unstaged changes or staged changes) the rebuild button will become orange and say 'dirty'. If the repo is clean there is a periodic check for remote changes and a 'pull --rebase' if there are.

When the rebuild button is active pressing it will cause LR to run 'make deploy-dev-binaries' at the repo root (and indicate while this is progressing).

LR also has an 'auto-rebuild' toggle available. This causes the rebuild to happen automatically when it is detected as possible. The check-and-rebuild process is single-threaded to avoid conflicts.


prompt: [`0eab75d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0eab75d317e400785f6a359d08e66921d4bac1f1) → reply: [`a87e2ba`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/a87e2ba1645c2cbee7376a6f6a748934a38d92bb)

## Reply

Implemented Step 3: added local-representative's dev-repo watcher (`--dev-repo`, implying `--dev-mode`, refusing to start outside a git repo) as a new `repowatch.go`, polling the watched repo every 5s to drive a system-tab **rebuild** control that turns orange/"dirty" on uncommitted changes or green/"rebuild" once HEAD has moved (auto-pulling a clean repo's upstream with `pull --rebase` first), runs `make deploy-dev-binaries` at the repo root on press (showing "building…"), and offers an **auto-rebuild** toggle — all serialized through one mutex so the check-and-rebuild process is single-threaded, plus matching `agent-coordinator` `__system:rebuild`/`__system:auto-rebuild` remote commands. Wired the full stack: WebSocket `repo-state`/`rebuild-app`/`set-auto-rebuild` messages, the React system-tab UI and CSS, `docs/DevMode.md`/`local-representative/README.md` documentation, and new Go tests (`repowatch_test.go`) exercising the logic against real temporary git repos. As with every prior reply in this sandbox, `go build`/`go test`/`tsc` couldn't actually be run (toolchain approval denied), so this was verified by careful manual review instead.


## Revision A

Let's add the ufa-loader binary to the .gitignore.

Let's also fix the 'rebuild' control:
- We should not see the illuminated button when the HEAD has not moved (currently we see it even with no changes)
- When the repo is 'dirty' we should not be able to select the control


prompt: [`9819f4c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/9819f4c415722c954395f0afae418b4359b4499c) → reply: [`6b8dca8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/6b8dca828e189604da5c0c1f4f3c2187a7879650)

## Reply A

These are unrelated (Makefile target references, not the binary path), so no further changes needed.

## Summary

For the rebuild-control fix I changed `rebuildReadyLocked` in `repowatch.go` from `dirty || head != builtHead` to `!dirty && head != builtHead`, and seeded `builtHead`/`head` from the actual HEAD when the watcher starts (`newRepoWatch`) instead of leaving `builtHead` empty — this stops the button lighting up on launch when nothing has actually changed, and now makes the button disabled (not just orange) whenever the repo is dirty, since `requestRebuild`/`maybeAutoRebuild` both gate on the same flag. Updated the Go tests, `App.tsx`'s tooltip/doc comment, and `docs/DevMode.md`/`local-representative/README.md` to match the corrected behavior.

For the `.gitignore` request I added `ufa-loader/.gitignore` (matching the `# Binary\n<name>` pattern every other sub-project uses). One caveat: `ufa-loader/ufa-loader` is currently a tracked file in this repo (unlike the other components' binaries), and untracking it needs a `git rm --cached ufa-loader/ufa-loader`, which this sandbox blocked me from running (all state-mutating git commands here return "requires approval" with no prompt reaching me) — you'll need to run that yourself for the new `.gitignore` entry to actually take effect. As with prior replies, `go build`/`go test`/`tsc` also couldn't be run here, so this was verified by manual review.


## <REPLACE-Revision|Retry> B

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
