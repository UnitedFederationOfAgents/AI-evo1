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


## <REPLACE-Revision|Retry> D

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
