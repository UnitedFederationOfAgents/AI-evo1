# TODO

## Known bugs

### federation-command auto-connect adopts the wrong control mode

**Status:** open — fix attempted (condoc `AutolaunchChains`, Step 1 Revision A) and reverted as ineffective.

**Context:** `./federation-command --auto-connect` dials local-representative in the
background on startup. When the connection lands, FC decides whether to enter
*remote* control (LR drives) or *local* control (the terminal keeps driving).

**Intended behaviour:** if the blinker dot is selected (would-be remote mode) when
the connection completes, FC should enter remote control; if the connection
completes while the operator is at the normal entry prompt, FC should enter local
control and leave the foreground session untouched.

**Actual behaviour:** Revision A added `autoConnectControlState()` in
`federation-command/main.go` plus a local-control branch in the
`autoConnectResultMsg` handler, but in practice the CLI is still yanked out of
local mode and into remote when the background auto-connect succeeds. The gate is
not distinguishing the two situations correctly (suspect: `Blinker.IsSelectMode()`
/ `IsConnecting()` state at the moment the background dial resolves, and/or the
`autoConnectResultMsg` handler unconditionally taking the remote path).

**Next steps:**
- Re-verify what `Blinker` state actually holds while the operator is mid-entry vs.
  dot-selected, and confirm `autoConnectControlState()` is being consulted (not
  bypassed) on the success path.
- Add a focused test that drives `autoConnectResultMsg` with each blinker state
  and asserts the resulting `input.Focused()` / sent `SendState(...)` value.
- Consider deferring the control-mode decision to the first keypress after connect
  rather than deciding at connect time.
