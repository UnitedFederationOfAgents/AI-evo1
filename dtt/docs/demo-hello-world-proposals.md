# DTT: Proposed hello world demos

**Status: proposals for discussion. None of this is implemented.** The
implemented demos are the session demo (`localrep-demo-session-full`) and
the condocv2 demo (`localrep-demo-condoc`) in [`../examples/compositions/`](../examples/compositions/);
the README's "Calls and phases" and the localrep tool's demo notes cover
them.

Each proposal below has the same six phases as those two:

| phase | does | example it calls |
|---|---|---|
| `prepare` | wake the node, put the demo page on its desktop, switch to an empty workspace | `localrep-demo-prepare` (as is) |
| `<app>-prepare` | **set-up**: make sure the app runs; a federation-command terminal on the left half, a browser of its own (throwaway profile `/tmp/dtt-demo-<app>-<at>`, zoomed 1.5x) on the app through agent-coordinator on the right half | `localrep-demo-<app>-prepare` (new) |
| `hello` | **hello world**: at least one action from the terminal seen in the UI, and one from the UI seen from the terminal | `localrep-demo-<app>-hello` (new) |
| `<app>-shut-down` | **tear-down**: undo the hello's leftovers, close the browser by its profile, have the terminal `exit` | `localrep-demo-<app>-shutdown` (new) |
| `shut-down` | delete the page, back to the first workspace | `localrep-demo-shutdown` (as is) |
| `compose` | the five renders, each after a chapter card, between a title and a closing card | -- |

The set-up and tear-down are the session demo's with the app swapped in
(`ensure-app`, the page the robot types into the address bar, the text it
waits for), so each proposal below only says what differs. Throughout:

- **Pages.** composer, The Conversationalist, the robot and session-manager
  have pages of their own through agent-coordinator,
  `<ac>/host/<node>/{compose,convo,robot,sessions}/`. The tools and files
  tabs don't: `<ac>/host/<node>/` opens local-representative on its last
  tab, so the robot clicks the tab's button (labelled `tools`, `files`) --
  exact matches, since the words appear elsewhere on the page.
- **The terminal shows what it types.** The robot reads the whole screen,
  so no terminal command should hold the text the robot waits for, or the
  robot should count first and take only a new line (`count-text`, then
  `new_since`), as the session hello does.
- **One shell per command.** Each line federation-command runs is its own
  `bash -c`, so anything a later command needs is kept in a file under
  `/tmp` named by `<at>` (the condocv2 hello does this).
- **Ports on the node.** local-representative 8081, composer 8089, The
  Conversationalist 8086, the robot 8087. A terminal curls these on
  `localhost`; files uploaded through agent-coordinator's proxy are refused,
  so uploads go to local-representative directly.

## composer

- **Set-up.** `ensure-app` `compose`; the robot types
  `<ac>/host/<node>/compose/` and waits for "Composer".
- **Hello world.**
  1. *Terminal → UI.* The terminal `PUT`s a small composition to
     `localhost:8089/api/compositions/hello-world-<at>`: a 3s title card,
     "hello world from <node>, from its terminal". The composition list
     only loads when the page does, so the robot reloads (F5) and clicks
     the new name, waiting for its title text in the editor.
  2. *UI → terminal.* The robot clicks "● Render" (keep the tab visible:
     the browser renderer records it on screen). The terminal polls
     `localhost:8081/api/files` until `composer-hello-world-<at>-*.webm`
     appears and prints its size.
  3. The terminal says hello world.
- **Tear-down.** The terminal `DELETE`s the composition, and the render
  from local-representative's files (`DELETE /api/files/<id>`), before
  closing the browser and itself.
- **Weak spots.** "● Render" starts with a symbol the screen reading may
  drop; match "Render" exactly or not at all. Rendering in the browser
  takes the composition's length plus a few seconds.

## federation-command

The terminal *is* the app here, so the browser shows local-representative's
federation-command tab, which mirrors every terminal.

- **Set-up.** No `ensure-app` (the terminal comes from `launch-fc`). The
  robot types `<ac>/host/<node>/`, clicks the `federation-command` tab
  button, and picks the new terminal's instance from the instance picker
  (it shows `federation-command#N`, the prep's `fc`).
- **Hello world.**
  1. *Terminal → UI.* The terminal runs `ufa host get` and
     `echo "hello world from <node>, from its terminal - <token>"`. The tab's
     log shows every command and its output, so the robot waits for a
     second line with the token (the terminal's own being the first:
     `count-text`, then `more_than`).
  2. *UI → terminal.* In remote control the tab has a command box ("enter
     command to run on federation-command…", then "run"). The robot clicks
     it, types `echo "hello back from the browser - <token>"` and presses
     Enter; the run's `fc-expect-output` waits for the token from the
     terminal itself, and the robot sees it there too.
  3. The terminal runs `ufa session get`, so the recording ends on both
     windows naming the same session.
- **Tear-down.** As the session demo's. Nothing is left behind but the
  terminal's records.
- **Weak spots.** The instance picker's labels are small; if the robot
  can't read them, leave the tab on whichever instance it opens and say so
  in the chapter card.

## robot

The robot drives the demo already; this one shows its own page.

- **Set-up.** `ensure-app` `robot`; the robot types
  `<ac>/host/<node>/robot/` and waits for "I am Not a Robot".
- **Hello world.**
  1. *Terminal → UI.* The terminal writes
     `/tmp/dtt-demo-robot-<at>.txt` holding "hello world from <node>, from
     its terminal" and opens it in a text editor beside the browser
     (`setsid xdg-open`, or the `robot.open` op with an `app`). The robot
     waits for the line on screen (`wait-for-text`, after a `count-text`
     to skip the terminal's own command), which is the robot reading what
     the terminal wrote.
  2. *UI → terminal.* The robot clicks "Inspect Screen" on its own page;
     its result lists what it read. Then `robot.save-screenshot` saves a
     screenshot to `/tmp/dtt-demo-robot-<at>.png`, and the terminal checks
     it exists and prints its size (`file`, `stat`).
  3. The terminal says hello world.
- **Tear-down.** Close the editor (`ctrl+w`, or `pkill` by the file's
  name), remove both `/tmp` files, then as the session demo's.
- **Weak spots.** A third window breaks the left/right tiling: open the
  editor over the terminal's half, or skip it and have the robot read the
  terminal's own `cat` instead (less of a demo, but nothing to tile).

## The Conversationalist

- **Set-up.** `ensure-app` `convo`; the robot types
  `<ac>/host/<node>/convo/` and waits for "The Conversationalist", then
  "Notepad".
- **Hello world.** Without AWS (Polly costs money, and needs credentials):
  1. *UI → terminal.* The robot clicks the notepad ("Quick notes — visible
     to everyone connected."), types "hello world from <node>, from the
     browser" and uses "To Notes", which appends it to `~/notes-input.txt`
     on the node. The terminal `tail`s that file until the line is there.
  2. *Terminal → UI.* The notepad has no HTTP way in, so the terminal's
     side is Polly: `curl -X POST localhost:8086/api/polly` with
     `{"text": "hello world from <node>, from its terminal", "name":
     "hello-world-<at>", "play": true}`. The browser plays it (the
     recording has no sound, so the robot waits for `polly-hello-world-<at>.mp3`
     in the page's clip list). This step is optional, behind a variable
     (`polly=false` by default), for nodes with AWS.
  3. The terminal says hello world.
- **Tear-down.** Remove the demo's line from `~/notes-input.txt` (the
  terminal knows it exactly), clear the notepad (it is shared live with
  every open tab), delete the Polly clip from files if made, then as the
  session demo's.
- **Weak spots.** "To Notes" and the notepad's grey placeholder are small;
  the session hello's note box has the same problem. Check which control
  "To Notes" belongs to before relying on it (transcription or notepad).

## tools

local-representative's tools tab: calls, history, configure.

- **Set-up.** No `ensure-app`. The robot types `<ac>/host/<node>/`, clicks
  the `tools` tab button and then `history`, and waits for its hint
  ("Every dtt call … recorded on this node").
- **Hello world.**
  1. *Terminal → UI.* The terminal runs a harmless dtt call,
     `dtt read examples/compositions/filesystem-json.dtt.yaml` (or a T3 run of a
     built-in that only echoes) with a variable naming `hello-world-<at>`.
     dtt records it under `$AGENT_RECORDS_PATH/dtt-calls/`. The robot clicks
     "reload" and waits for the call's line in the history.
  2. *UI → terminal.* The robot opens `calls` and runs the same tool from
     the tab with its own marker, "hello back from the browser". The
     terminal waits for a new file in `dtt-calls/` holding that marker and
     prints its first lines.
  3. The terminal says hello world.
- **Tear-down.** Nothing to undo: dtt calls are records. As the session
  demo's.
- **Weak spots.** The history lists calls by tool and time, not their
  inputs; if the marker isn't on screen, the robot has to click the entry
  open first. A node started with `--tools-allow` may not offer the tool.

## files

local-representative's files tab.

- **Set-up.** No `ensure-app`. The robot types `<ac>/host/<node>/`, clicks
  the `files` tab button and waits for "drag files here, or click to
  browse".
- **Hello world.**
  1. *Terminal → UI.* The terminal writes `hello-world-<at>.txt` ("hello
     world from <node>, from its terminal") and uploads it,
     `curl -F file=@… localhost:8081/api/files`, then highlights it
     (`POST /api/files/<id>/highlight`). The tab lists files live, so the
     robot waits for the name.
  2. *UI → terminal.* The robot clicks "+ new text file", types
     `hello-back-<at>.txt` and "hello back from the browser", and saves.
     The terminal polls `GET /api/files` for it, downloads it
     (`/api/files/<id>?download=1`) and prints its text.
  3. The terminal says hello world.
- **Tear-down.** The terminal deletes both files (`DELETE /api/files/<id>`);
  they would go within the hour anyway. Then as the session demo's.
- **Weak spots.** The new-text-file form's fields haven't been driven by
  the robot before; tab order needs checking on a real screen. The demo's
  own recordings and renders are in the same list, so keep names
  distinctive.

## Order

composer, files and tools need nothing beyond what the demos already
assume and touch nothing outside the node, so they are the cheapest next
steps. federation-command depends on reading the instance picker. The
robot's adds a third window. The Conversationalist's terminal side needs
AWS, or a notepad API that doesn't exist yet.
