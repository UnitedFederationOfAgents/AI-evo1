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


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
