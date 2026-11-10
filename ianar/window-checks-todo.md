# Window checks: what failed, and what's next

dtt's `localrep-demo-prepare` example (`../dtt/examples/compositions/localrep-demo-prepare.dtt.yaml`)
has to get a node's screen clear before a demo. It first tried to do that by
minimizing every open window and checking, through the window manager, that
none was left open. That never worked on the target node, so the code for it
has been removed. The example now just switches to an empty workspace.

## What we tried

The robot had two ops, `count-windows` and `minimize-windows`. It pressed
`super+d` (show the desktop) and then `super+h` on each window still open,
listing the windows after each round to check. Everything depended on
listing windows, and on the target node (GNOME on Wayland, nothing extra
installed) every way of listing them failed:

| Path | Why it failed on the node |
| --- | --- |
| gnome-shell `org.gnome.Shell.Eval` | refused unless gnome-shell runs in unsafe mode |
| "Window Calls" extension (`org.gnome.Shell.Extensions.Windows`) | not installed |
| gnome-shell `org.gnome.Shell.Introspect` | refused while gnome-shell's `introspect` setting is off; the robot tried to turn it on for each op, but this gnome-shell has no such setting (`No such key "introspect"`) |
| X11 `xprop` / `wmctrl` / `xdotool` | not an X11 session |
| the apps' own windows over AT-SPI, judged by which titles OCR could read on screen | added last; it didn't work on the node either |

The underlying problem: on Wayland, gnome-shell doesn't tell other programs
which windows are open unless the node is changed (unsafe mode, an
extension, or a setting this version doesn't have). We wanted the node left
as it is, so we stopped trying.

## What was removed

- `windows.go`, `windows_linux.go`, `windows_atspi_linux.go` and their tests
  (`windows_test.go`, `windows_linux_test.go`)
- the `count-windows` and `minimize-windows` ops in `seqv2_ops.go`
- the example's `each_keys` and `introspect` variables, and the README's
  notes on listing windows

`focus-window` (`window.go`, `window_linux.go`) was there before and stays.
It finds a window by its title on screen and only falls back to the window
manager. LR's `ensure-app` op stays too, because the example still uses it
to start the robot.

## What the example does now

After starting the robot and waking the screen, the robot presses
`super+end` (GNOME's "switch to last workspace"). GNOME's dynamic
workspaces, which are on by default, always keep an empty workspace at the
end, so the screen is clear without any window being touched. The windows
stay on the workspace left behind.

## Next steps

1. **Check that the workspace is really empty, without the window
   manager.** Nothing checks it yet; the recording is the evidence. Options,
   cheapest first:
   - Before switching, OCR the titles on screen. After switching, fail if
     any of them can still be read (outside the top bar). This needs a small
     new op, or a `count-text` variant that takes a list of texts.
   - Fail if the screen didn't change at all, which would mean the keys
     aren't bound or the press went nowhere.
   - Compare the screen with the wallpaper
     (`gsettings get org.gnome.desktop.background picture-uri`, which only
     reads a setting). This is fragile with scaling and with more than one
     monitor.
2. **Handle nodes without dynamic workspaces** (`org.gnome.mutter
   dynamic-workspaces` false). There the last workspace may not be empty.
   Reading that setting only reads it, so the sequence could check it and
   fail with a clear message.
3. **Add a teardown DTT that returns to the original workspace** (see
   below). Done as `dtt/examples/compositions/localrep-demo-shutdown.dtt.yaml`
   (`super+home`, and the demo page deleted with `delete-file`'s
   `missing_ok`); `localrep-demo-complete.dtt.yaml` runs prepare, a demo
   and the shutdown in turn. Returning to the exact workspace left, rather
   than the first, is still open.
4. **If a real window count is ever needed**, it means changing the node.
   The least invasive choice is the Window Calls extension
   (`gnome-extensions install`, per user, no root). If we take it, the
   removed code is in git history (`ianar/windows*.go`, before this change),
   and the Window Calls path there was tried first.

## A teardown DTT

A matching "tear down the demo workspace" DTT is plausible because
preparing the node doesn't destroy anything:

- **Nothing to restore.** No window was closed or minimized, so undoing the
  preparation is only a matter of going back to the workspace we left.
- **GNOME cleans up after itself.** With dynamic workspaces, an empty
  workspace you leave is removed. If the demo didn't open anything there,
  switching away is the whole teardown.
- **Getting back needs no window list.** The sequence can't ask which
  workspace it came from, but keys can take it back:
  - `super+home` goes to the first workspace, which is right when the user
    works there.
  - Alt+Tab is the more general option. GNOME's switcher covers all
    workspaces by default and goes to the most recently used window's
    workspace, which is the one we left. This is untested on the node.
- **The demo's own windows are knowable.** Anything the demo opened on the
  empty workspace was opened by its own sequence, so a teardown sequence (or
  an LR capability for the apps LR launched) can close exactly those,
  without listing anything.
- **It fits the existing pieces.** A `dtt-demo-teardown` sequence can live in
  the same `resources/demo-prepare.control.yaml`, and a
  `localrep-demo-teardown.dtt.yaml` example can run it with the same
  `node`, recorded and composed like the prepare one. Another option is to
  have the prepare example's `delete` run the teardown, but that needs a
  dtt engine change: today deleting keeps runs and doesn't start new ones.
