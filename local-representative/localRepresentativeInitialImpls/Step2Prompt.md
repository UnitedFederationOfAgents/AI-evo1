# Prompt

[LocalRepresentativeInitial](../LocalRepresentativeInitial.md)

During this step we will first look at our options for implementing integration between federation-command functions like ridealongs and condoc mode.

We want to have as similar of an experience as possible on the web UI compared to the terminal UI. The user maintains the same paradigm of selecting between local and remote control, and both consoles are able to see the selectable option and which option the other console selects as they select it (when permitted due to local/remote control).

First, in a pure-response capacity with no code updates, we will describe three paths to implementation with distinct levels of effort/comprehensiveness.


[`0cfed8c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0cfed8c0d28594f95c61c36e03786e6bf69d71b1)

## Reply

Now I have a thorough understanding of the architecture. Here are three implementation paths:

---

## Three Paths: Ridealong & Condoc Integration in the Web UI

### Context

Currently, the local-representative web UI shows federation-command's control state (`remote-control` / `local-control`) and live logs via the representable protocol. Ridealong and condoc modes are entirely terminal-side: rich TUI state machines living in `federation-command/ridealong.go` and `federation-command/condoc.go`, with no serialization to the representable layer. The goal is for both the web console and the terminal console to see and interact with these modes with the same local/remote control paradigm.

---

### Path 1 — State Visibility Only (Low Effort)

**What it does:** The terminal remains the only place where ridealong and condoc sessions are *driven*, but the web UI gains read-only awareness of which mode is active and where in it the session is.

**Protocol changes:** Add two new representable message types:
- `ridealong-state`: emitted on step changes — current file, current step index, step title, waypoints list, autoplay status.
- `condoc-state`: emitted on phase changes — condoc name, current phase, current step/substep number, last action taken.

**Web UI changes:** A small status banner in the federation-command tab showing mode name and current position (e.g., *"Ridealong: setup-guide.md — Step 3 / 7"* or *"Condoc: LocalRepresentativeInitial — Step 2 · agent_running"*). The local/remote control toggle and command-relay input continue to work as today; these banners are display-only.

**Trade-offs:**
- Very fast to ship. Zero new paradigm surface — you're just extending the log stream with structured metadata.
- Web users can observe but cannot participate. Good enough for monitoring from a second screen; not good enough if you want the web console to be a first-class driver.

---

### Path 2 — Interactive Control Panels (Medium Effort)

**What it does:** The web UI gets button-based equivalents of the TUI keyboard menus for both ridealong and condoc, synced bidirectionally via the representable connection. Both consoles can see state in real time and, based on the current local/remote control setting, one of them has the "active pen."

**Protocol changes:** Build on Path 1's state messages and add:
- `ridealong-command`: web → FC; payloads like `{"action": "next"}`, `{"action": "prev"}`, `{"action": "waypoint", "letter": "b"}`, `{"action": "autoplay-toggle"}`, `{"action": "custom-cmd", "cmd": "..."}`.
- `condoc-command`: web → FC; payloads mirroring existing condoc actions — `handoff`, `revision`, `retry`, `substep`, `start_step`, `revert`.
- FC's ridealong and condoc handlers consume these from the `remoteCmdCh` (or a parallel command channel) exactly the same way they consume keyboard events.

**Web UI changes:** When the FC tab detects `fcState === "remote-control"` and a ridealong/condoc state message is present, replace the generic command-relay input with a context-specific control panel:
- **Ridealong panel:** prev/next buttons, waypoint list as clickable items, autoplay toggle with countdown display, custom command input field.
- **Condoc panel:** current step/phase display, action buttons matching the condoc phase (e.g., in `awaiting_action`: handoff, revision, retry, substep buttons — matching what condoccer already shows).

**Key invariant to maintain:** FC's existing blinker states `BlinkerRidealong` and `BlinkerCondoc` already represent the sub-mode. The local/remote control toggle semantics don't change — `local-control` means FC ignores incoming commands and the web panel goes read-only (Path 1 visibility); `remote-control` means the web panel is live.

**Trade-offs:**
- Substantial but bounded scope. No new server processes, no file I/O from the web side — all state mutation still happens inside FC.
- The biggest risk is state drift: if a step transitions inside FC and the representable connection hiccups, the web panel could show stale state. Mitigation: FC re-emits full state on reconnect, and the web panel requests a refresh on WebSocket re-open.
- Condoc's condoccer already implements this panel conceptually — you're duplicating some React UI, but you avoid coupling two separate backend services.

---

### Path 3 — Full Dual-Console Parity (High Effort)

**What it does:** Both consoles are fully symmetric first-class participants. Either console can *initiate* a ridealong or condoc session (not just join one in progress). The web UI can browse and select ridealong markdown files and condoc directories. Condoccer's functionality is absorbed into local-representative rather than living as a separate server. The local/remote control paradigm is extended to a "session ownership" model: either console can claim ownership of the active ridealong/condoc at any step boundary, and ownership transfers are visible to both sides.

**Architectural changes:**
- **Session ownership protocol:** Add an `owner` field to ridealong-state and condoc-state messages (`"terminal"` or `"web"`). The current owner drives; the observer sees but cannot act. Either console can request a transfer at a step boundary (web sends `ridealong-command: {"action": "claim-ownership"}`; FC either grants it by updating state or rejects if mid-step).
- **File system access from web:** Local-representative exposes a small REST API (`GET /files?path=...`) so the web UI can browse the workspace to select a ridealong file or condoc directory. FC then receives an `initiate-ridealong` or `initiate-condoc` command with the path. This is the most novel trust boundary — FC must validate the path before launching.
- **Condoccer absorption:** Move condoccer's file-watching WebSocket logic into local-representative as a Go package. The condoc tab in local-representative subsumes what condoccer does today, so there's a single dashboard for condoc state with both the condoccer-style file content view and the control panel from Path 2. The standalone condoccer binary can remain for backward compatibility.
- **TUI mirroring stream (optional enhancement):** Pipe the bubbletea rendered frame (as text) into a `tui-frame` representable message so the web console shows a live terminal replica alongside the structured panels. Adds fidelity but is optional — the structured panels from Path 2 are more maintainable.

**Trade-offs:**
- Highest fidelity — the two consoles are genuinely interchangeable and a user can hand off mid-ridealong to a colleague watching the web UI.
- Highest risk: file system exposure from the web UI introduces a new attack surface (path traversal, unauthorized reads); the ownership-transfer protocol must handle races at step boundaries cleanly; absorbing condoccer couples two previously independent systems.
- Recommended only if the end goal is a fully web-native workflow where the terminal is optional, not the primary driver.

---

### Summary Table

| | Path 1 | Path 2 | Path 3 |
|---|---|---|---|
| **Effort** | Low (days) | Medium (1–2 weeks) | High (3–6 weeks) |
| **Web can observe mode** | Yes | Yes | Yes |
| **Web can drive ridealong/condoc** | No | Yes (when remote-control) | Yes (with ownership model) |
| **Web can initiate sessions** | No | No | Yes |
| **Condoccer integration** | None | Partial (duplicate React panel) | Full absorption |
| **New attack surface** | Minimal | Low (command relay, same as today) | Moderate (file browser API) |
| **State sync complexity** | Low | Medium | High |

The most natural progression is to ship Path 1 immediately (it's almost free given the existing representable infrastructure), then build Path 2 incrementally on top once Path 1's state serialization is validated in practice.

---

I described three implementation paths — Path 1 (read-only state visibility), Path 2 (interactive control panels driven from the web UI), and Path 3 (full dual-console parity with session ownership and condoccer absorption) — each building on the previous. The analysis is grounded in the existing representable protocol, blinker state machine, and the current ridealong/condoc TUI architecture, with no code changes made.


## Revision A

We are now going to implement a solution most similar to path 2.

We will also make sure the new panels are dynamic and only appear when the connected FC is in the context of that panel.

Additionally -- let's create an example-ridealong.md and example-child-ridealong.md in federation-command/docs/ -- these will be ridealongs that simply explain and demonstrate ridealongs, and will perform commands like 'echo' which have minimal side-effects as to be non-intrusive.


[`4e485ef`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/4e485ef0e716d8e7b4d11cec396c48c6346d6f0e)

## Reply A

Everything looks good. The implementation is complete. Here's a summary of what was implemented:

**Protocol layer** (`representable/representable.go`): Added `DataType` and `Data` fields to `Msg`, a `SendData` method on `Client`, a `SetDataHandler` on `Server`, and `"data"` case handling in `handleConn`.

**FC backend** (`federation-command/main.go`, `condoc.go`): Added `ridealongStatePayload` and `condocStatePayload` structs, `sendRidealongState()` and `sendCondocState()` methods that push state over the representable data channel, and `handleRidealongRemoteCmd()` that intercepts `__ridealong:ACTION` commands (execute, exit, autoplay, waypoint:NAME, custom:CMD). These are called at all key state transitions — ridealong start/advance/exit/waypoint-jump and every condoc phase change.

**LR server** (`local-representative/main.go`): Added `RidealongStateMsg` / `CondocStateMsg` types, per-session state fields on `Server`, a `SetDataHandler` that receives and rebroadcasts ridealong/condoc state to all browsers, `ridealong-command` handling in the browser read pump (forwarding as `__ridealong:ACTION` to FC), and initial state delivery on new WebSocket connections.

**Frontend** (`types.ts`, `App.tsx`, `index.css`): Added `RidealongStateMsg` and `CondocStateMsg` interfaces; `RidealongPanel` (shows prev/current/next step, execute/autoplay/exit buttons, waypoint buttons, custom command input — all hidden when FC is not in remote-control) and `CondocPanel` (shows phase and step number) that appear dynamically only when FC is in the relevant mode; complete CSS styling for both panels in the terminal dark-mode aesthetic.

**Example files** (`federation-command/docs/example-ridealong.md`, `example-child-ridealong.md`): Self-contained ridealongs using only `echo` commands, with waypoints and a parent→child dive link, designed to demonstrate and explain ridealong mechanics non-destructively.


## Revision B

This implementation is a good start, but has some bugs.

In this iteration we will correct the following bugs:
- When we are in remote control and we send a command to enter a ridealong from the web console the ridealong begins in local-control instead of remote.
- When we end a ridealong it kicks us out of connected mode but it should not
- When we are executing a ridealong in local-control we cannot see the output appearing in the output window of LR


[`1d708f4`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/1d708f44745e1d1d68b74202208b5e4d68950ce5)

## Reply B

All six edits are in place and look correct. The changes are entirely in `federation-command/main.go`:

1. **Bug 1** — Moved the `__ridealong:` intercept before the `IsConnected()` gate in the `reprRemoteCmdMsg` handler, allowing ridealong control commands through whenever the blinker is in `BlinkerConnected` or `BlinkerRidealong` mode. This means web-console ridealong controls work after FC transitions into ridealong mode.

2. **Bug 2** — Added a `ridealongPrevState BlinkerState` field to `appModel`. Before entering ridealong mode (`executeCommandCore`) the current blinker state is saved. In `exitRidealong()`, the prior state is restored: `BlinkerConnected` returns to remote-control (with `SendState("remote-control")` to LR and input blurred), `BlinkerLocalControl` returns to local-control (input focused), and any other state falls back to idle. This keeps the LR connection alive after a ridealong ends.

3. **Bug 3** — Changed both `buildRunCmd(... "")` calls in `executeRidealongCommand` and `executeRidealongCustomCmd` to pass `m.reprOutPath`, and added `sendNewOutputToRepr` forwarding in both `ridealongCmdDoneMsg` and `ridealongCustomCmdDoneMsg` handlers. Ridealong command output is now tee'd to the repr file and forwarded to the LR output window.


## Revision C

This implementation is another step in the right direction.

We are now able to see and control ridealongs in the LR console successfully in general.

We have a couple of bugs to fix again, however:
- We have regression in FC now where when we are in the standard entry portion of the CLI up and down are changing states when they should not be. (They should be allowing us to cycle through previous commands)
- When we enter child ridealongs we do not see the update immediately, we have to wait for another step change before the view corrects itself

Let's also adjust our 'agent' call in our new example ridealong so that it invokes clod rather than a real agent.


[`1b8a0a9`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/1b8a0a9bb9485a6b30d9c1fa48f36262b6dad867)

## Reply C

All three changes are in place and look correct.

**Summary of changes made:**

1. **Bug 1 (up/down changing states in standard CLI entry):** Removed `m.blinker.IsLocalControl()` from the disconnect guard in both `handleHistoryUp()` and `handleHistoryDown()` — local-control mode now falls through to history cycling instead of disconnecting. Also guarded the `BlinkerInactive` state assignment with `!m.blinker.IsRemoteControlActive()` so the local-control blinker state is preserved while cycling history.

2. **Bug 2 (child ridealong state not updating immediately):** Added `m.sendRidealongState()` in `enterDiveStep()` right after `m.ridealong = child` is set, matching the pattern used at every other ridealong state transition site.

3. **Example ridealong agent call:** Changed `agent -p "..."` to `clod -p "..."` on the relevant ridealong step in `federation-command/docs/example-ridealong.md`.


## Revision D

When we finish a ridealong we have initiated from local-control we correctly enter a standard cli entry local-control state functionally, but the blinking cursor shows grey when we expect blue. When we press left to enter state transition mode we also see that we do not re-enter remote control. When we change state to 'connected' again we see the connection is interrupted and re-initiated. Let's fix this.

We have also changed the 'agent' command in our example ridealong directly to 'clod' -- we want to use an argument in the agent command to select that agent instead. Let's correct that too.


[`c65cd1f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/c65cd1f63d3e96988d91b3afe13de1022de76281)

## Reply D

All changes look correct. The `textinput` package is already imported (used in `Init()` and `handleRight()`), so no import changes are needed.

Here's a summary of what was changed:

**Bug 1 (grey cursor / left-key state issue):** In `exitRidealong()`, the `BlinkerLocalControl` case now calls `m.reprClient.SendState("local-control")` (if connected) before restoring the blinker — mirroring the behavior of the normal right-arrow local-control entry path. Additionally, `textinput.Blink` was added to the returned batch so the text cursor is properly re-animated on exit, matching the same pattern used when entering local-control normally and at FC startup.

**Bug 2 (agent command for ridealong):** `buildAgentCmd` now recognises a `-a AGENT` flag that overrides the current session agent for that invocation. The `example-ridealong.md` step was updated from the bare `clod -p "..."` invocation to `agent -a clod -p "..."`, keeping it within the FC `agent` command paradigm while still targeting the clod agent explicitly.


## <REPLACE-Revision|Retry> E

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
