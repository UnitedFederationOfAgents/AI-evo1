# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Now that we have the requisite sub-components in place we create the mechanism tying everything together.

For a first step we will introduce the condoc lock file.

This file is named '.condoc' and may exist in the root of the repo. The condoccer will create this file whenever it begins working on a new condoc - whenever a state transition other than the completion of the agent's work occurs the file's contents will be updated to:
"Condoccer <performed action> at <human-readable-datetime> (<unix-timestamp>)"

When a condoc enters an "awaiting action" phase specifically after an agent completes work this file will be removed. (Before being re-added again at the next state transition)

The point of this file is to prevent excessive rebuilds.

When this file is present the 'rebuild available' signal will always be false for LR.

When a condoc is complete the file will be removed.


prompt: [`7ef3cd7`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/7ef3cd713a4027990ca16b5653d0b3b0ac0e3dfc) → reply: [`537c414`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/537c4144badc74af3de92816664496abf694e25e)

## Reply

I've implemented Step 5's `.condoc` lock-file mechanism end-to-end. On the condoccer side, `watchLoop` now tracks each condoc's phase across polls and creates/updates/removes a `.condoc` file at the repo root: it's written whenever condoccer first sees a condoc (unless already safely at `awaiting_action`/`completed`) or when a condoc transitions to any other phase, and it's removed exactly when a condoc reaches `awaiting_action` right after an agent finishes, or reaches `completed`. On the local-representative side, `repoWatch.rebuildReadyLocked()` now checks for that file's presence and forces the rebuild signal false whenever it exists, with a new `condoc_locked` field surfaced through both LR's and agent-coordinator's dev-repo UI (labeled "condoc" with an explanatory tooltip) so the disabled rebuild button is self-explanatory. I added unit tests for the lock transitions in condoccer and for the forced-not-ready behavior in LR, plus a `.gitignore` entry for the runtime-only file; I wasn't able to run `go build`/`go test` in this environment (the harness denies `go`/`make` invocations), so I verified correctness by careful manual read-through instead.


## Revision A

Let's make sure that the .condoc file is not .gitignored - we need nodes following the branch to be aware of the state.

When the condoc is completed the file will be removed, so we do not expect to find them reaching 'main'.


prompt: [`b24617a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b24617abc22975e49437758218b539b3a9d27543) → reply: [`b2ca4f1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b2ca4f1fd58fbaa5ecdcb6daf77da3a204e97576)

## Reply A

I removed the `/.condoc` entry (and its explanatory comment) from the root `.gitignore` so the lock file will now be tracked and committed like any other file, letting nodes following the branch see the condoc state. I confirmed there were no other gitignore entries or stray comments elsewhere in the repo (condoccer, local-representative, spec doc) claiming the file is never committed, so no other changes were needed.


## Revision B

Let's implement a browser refresh strategy to ensure our browser view is not accidentally out of date.

See 'condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md' for the strategy.


prompt: [`a90246c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/a90246ce50160d484c8294925fba774d7e2b17dc) → reply: [`46feaa6`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/46feaa62d99ab2f7d5a71926d92f37cd72914a34)

## Reply B

This all looks correct and consistent. This completes the implementation of Revision B.

## Summary

Implemented the browser-refresh strategy from `BrowserRefreshStrategy.md` across all three frontends (local-representative, agent-coordinator, condoccer): each `vite.config.ts` now bakes the repo-wide git version (via `compute-version.sh`) into the bundle as `__APP_VERSION__`, and each app's WebSocket handler compares that against the version the server reports on connect (LR's `system-state.self.version`, AC's and condoccer's `self-info.version`), calling `window.location.reload()` on mismatch while skipping the check under the Vite dev server. Since agent-coordinator's `SelfInfoMsg` didn't previously carry a version at all, I added that field to its Go struct and `selfInfo()`; I also added a `vite-env.d.ts` to each frontend for the new global and documented the mechanism in `docs/DevMode.md`. As before, `go`/`npm`/`tsc` invocations are denied in this environment, so I verified correctness via careful manual read-through rather than a build.


## Revision C

Let's implement a browser pickup strategy to ensure our browser view stays where it should be.

See 'condocs/initialDistributedDevelopmentImpls/BrowserPickupStrategy.md' for the strategy.


prompt: [`682cbad`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/682cbadc41fa1ddb3fd417d6fef714e3c0988c67) → reply: [`d69a136`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d69a13678343f34af4c344ba784511862c962054)

## Reply C

I implemented the browser pickup strategy from `BrowserPickupStrategy.md` across all three frontends. Condoccer (Layer 1) now mirrors its nav state (condoc/step/iteration/diff/file/hunk selection) into `location.hash` via `history.replaceState`, re-seeds that state on mount, catches up subscriptions/diff fetches once the WebSocket connects, and falls back to the condoc list if a hash-restored condoc turns out to be stale. Local-representative and agent-coordinator (Layer 2) now capture condoccer's iframe hash via `hashchange` and bake it back into the iframe `src` on remount, and persist their own active tab (agent-coordinator also persists the selected host) — all via `sessionStorage`. I also documented the mechanism in `docs/DevMode.md` alongside the existing "Browser refresh" section. As before, `go`/`npm`/`tsc` are denied in this environment, so I verified correctness via careful manual read-through rather than a build.


## Revision D

Let's revisit the '.condoc' lockfile process again briefly.

Currently we have a bug where the .condoc file is being deleted AFTER the commit, so the repo state is dirty.

We must delete this file immediately before we perform the commit upon completion of the agent's work.


prompt: [`6966bfc`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/6966bfc9a5bfdd1db37573805b5d6453104ac3c3) → reply: [`e2b5dfb`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/e2b5dfbf10f050787918ee29074998517fd105a4)

## Reply D

I already have the diff in context from the Edit — no need for further tool confirmation. The change is a small, self-contained addition (`_ = os.Remove(filepath.Join(cs.repoRoot, ".condoc"))` plus a comment) in `handleCondocAgentDone`, and both `os` and `path/filepath` were already imported, so this should compile cleanly (build tools remain denied in this environment, consistent with prior replies, so I verified via read-through).

Fixed the dirty-repo bug: I traced the ordering issue to a cross-process gap — `condoccer` only removes `.condoc` when its 1-second poll later notices a condoc reached `awaiting_action`, by which point `federation-command`'s `handleCondocAgentDone` (in `federation-command/condoc.go`) had already run `git add . && git commit && git push` for the agent's reply, so the lock file's removal was left as an uncommitted, unstaged deletion. I fixed it by having `handleCondocAgentDone` itself `os.Remove` the `.condoc` file immediately before building/running that commit sequence, so the deletion is captured by the same `git add .` and lands in the same commit as the agent's reply, keeping the repo clean at that commit boundary.


## Revision E

Now let's add another toggle - we will add this each in the per-host system tab, in the global system topology rebuild & restart controls, and in the global system topology agent-coordinator controls.

This toggle is 'auto-update' and it will cause a restart signal to be sent automatically when a host is ready to update upon restart. In the per-host cases it will only impact the single host. In the agent-coordinator case it will apply to any host ready to update.


prompt: [`3f00c5b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3f00c5bbadbdf0db6f89becbe8e32eca130431b1) → reply: [`0baf733`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0baf733a21dd9e3cc03b8e5fb622ca987a64cbe2)

## Reply E

The review agent found no compile-breaking, deadlock, nil-pointer, or prop-threading bugs, and confirmed the persistence-timing/restart-loop logic is safe. The implementation is complete.

Added an "auto-update" toggle (Step5Prompt.md Revision E) that automatically restarts a loader-managed LR once an on-disk update is detected, wired into three UI locations: local-representative's own system tab, agent-coordinator's per-host mirror, and agent-coordinator's global topology view (both the per-selected-host "rebuild & restart controls" and an aggregate toggle in the "agent-coordinator controls" section sweeping every connected loader-managed host — deliberately not touching agent-coordinator's own restart). Implementation spans `selfVersionWatch`'s new `autoUpdate`/`restart` fields (mirroring the existing auto-rebuild pattern, including firing immediately if an update is already available when toggled on), persistence of the flag across the very restart it triggers via `lrState`, new `set-auto-update`/`__system:auto-update` message plumbing through both Go backends, and full prop-threading through both React frontends, plus new unit tests and a `docs/DevMode.md` write-up. `go`/`npm` remain denied in this environment, so correctness was verified by careful read-through plus an independent subagent review rather than a build.


## Revision F

Let's revisit the "hold my page" functionality we implemented recently.

When we have the auto-restart-auto-refresh occur we are brought back to the main page for condoccer when looking at the AC UI. Let's either tweak that to make a fix if the problem is small or respond with why the rework would be extensive before continuing.


prompt: [`0b7c82b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0b7c82b9a2c11683f42025d57ef75bc931aff8ba) → reply: [`8455131`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8455131d8029549659d6ba529c80907ca04255dd)

## Reply F

I found and fixed the bug — it was small, no rework needed.

The "hold my page" (browser pickup) staleness-detection effect in `condoccer/frontend/src/App.tsx` was keyed off the generic `error` state, which gets set both by genuine server-side subscribe rejections *and* by routine `ws.onerror` transport hiccups during reconnect. Since every auto-restart/auto-refresh cycle causes exactly that kind of reconnect flakiness while the host is still coming back up (with `activeState` still null right after the reload), the effect was misfiring and treating a perfectly valid deep link as stale, bouncing condoccer back to the main list. I split this into a new `subscribeError` flag that's only set by an actual server-reported error message (not transport errors) and updated the staleness effect to key off that instead, plus tightened the corresponding `docs/DevMode.md` explanation. `npm`/`tsc` remain denied in this environment, so I verified via read-through.


## Revision G

To clarify, we want to fix the issue where we go back to the same portion of the same tab (for example - if we are viewing condoccer through AC then we want to return to the same step on the same condoc)

Let's fix this if it is low effort or explain why the effort would be high and re-asses.
