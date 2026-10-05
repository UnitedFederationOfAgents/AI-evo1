# Prompt

[InitialRobot](../InitialRobot.md)

Now that we have our first few working controls we will add 'Native Clip'.

The Native Clip button will capture a 4 second video clip of the desktop and present it for playback.


prompt: [`20ef02e`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/20ef02e66ac1af1a174f5617b26970c538d4b8a8) → reply: [`2c1f51a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/2c1f51aaa25a66bdf4c5d3d529853a0ce03c719a)

## Reply

I added the Native Clip button to IANAR: it records 4 seconds of the desktop and plays the clip back in the preview pane. I couldn't build or run it, because `go`, `tsc`/`npx` and `git status` all need approval in this sandbox, so it still needs a `make build` / `make test` on the host.

- **Recording:** it first uses gnome-shell's built-in screen recorder over D-Bus (`org.gnome.Shell.Screencast`) to make a real video file. GNOME shows its recording indicator in the top bar while it runs. Recent GNOME versions may refuse callers that aren't on an allowlist, as with the screenshot interface. In that case it falls back to grabbing frames about 10 times a second through the same paths Capture Native uses. Those frames are shrunk to at most 1280 px wide and sent as JPEGs.
- **Playback:** a video clip plays in a looping `<video>` player with controls. A frame-grabbed clip plays at its original pace, looping, with play/pause, a scrubber and a time readout. Whichever capture or clip finished last is what the preview shows.
- **Mouse circle during a clip:** a clip doesn't block Circle Mouse, so you can record the pointer circling. Only one clip can record at a time.
- **Files:** the new code is in `ianar/clip.go` and `ianar/screencast_linux.go`. I moved Capture Native's "try each path in turn" logic into a shared `grabNativeFrame` in `robot.go` so the clip can reuse it; Capture Native itself works as before. `main.go`, `App.tsx`, `types.ts` and `index.css` have the wiring and UI.
- **Tests:** new ones are in `ianar/clip_test.go`. They cover the recorder-first path, the frame fallback, a grab failing partway through, every path failing, a second clip while one is recording, shrinking frames, and picking the video type from the file extension.

If the recorder isn't allowed on this host, the fallback can only grab frames as fast as Capture Native's screenshot path allows, so those clips may come out choppy.

## Resource 1 -- Mobile Appearance

Screenshot

- [Screenshot_20261004-183624.png](0ab10dae_Screenshot_20261004-183624.png)

## Revision A

Let's improve the appearance in mobile browsers.

See the 'Mobile Appearance' resource for context.


prompt: [`dfd9a48`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/dfd9a48509605d674d6bb16e578e7e051ec757a6) → reply: [`46e4081`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/46e4081d3c3068843435d45ec416b707511e2a78)

## Reply A

I made IANAR stack its layout on screens 760 px wide or narrower, so the robot panel gets the full width of a phone. I couldn't build or check it: running `tsc`/`vite` needed approval again, so it still needs a `make build` on the host and a look on the phone.

- **Layout:** the sidebar now sits above the robot panel instead of taking 260 px beside it. This uses the same 760 px breakpoint as the other apps in the repo.
- **Buttons:** the four action buttons are now a 2×2 grid of larger, touch-sized buttons. In the screenshot, Circle Mouse and Native Clip were pushed off the right edge where you couldn't reach them.
- **Header:** on narrow screens the sidebar header gets extra top padding so the DEV MODE tag no longer covers it.
- **Smaller fixes:** the preview area has a minimum height, the connect and clip-player buttons are bigger, and the version tag no longer shows "vv0.0.3". That last fix strips a leading `v` in `App.tsx`.

The layout changes are in `ianar/frontend/src/index.css`.


## Revision B

Now let's take those controls and put them under a tab within ianar called 'simple'.

Let's also add a second tab called 'sequence-v1'.

The sequence tab will allow us to put high level actions in a sequence (which under the hood correspond to more detailed instructions).

To start we will have it populated with a sequence:
- Select the terminal with federation-command
- Bring federation-command to local control
- Bring the cursor to the command line input
- Enter: 'echo "hello world!"'
- Press enter to submit the command
- Bring federation-command back to remote control

The full sequence should be recorded.

The execution of the sequence should report success or failure.

Let's implement this now.


prompt: [`df296e1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/df296e1011cab654e8ee04819bc5479a567755b2) → reply: [`46f13c8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/46f13c8d001bd66e8363ea2568d1148625144474)

## Reply B

I added the two tabs to IANAR. **simple** holds the existing four controls. **sequence-v1** runs the six-step federation-command "hello world" sequence, records the whole run, and reports success or failure for each step and for the run. None of it has been built or tested: `go test`, `gofmt` and `tsc` all need approval in this sandbox. It needs a `make build` / `make test` on the host, and the new Go tests are in `ianar/sequence_test.go`.

- **What each step does:** it focuses FC's terminal window, presses Right (local control), presses End then Ctrl+U for an empty input line, types `echo "hello world!"`, presses Enter and waits 1s, then presses Left (back to remote control). The tab lists every step with the actions it performs and shows its status live. The run stops at the first failing step and marks the rest as skipped.
- **Keys:** sent through GNOME's remote-desktop input first, the same path Circle Mouse uses, with robotgo as the fallback.
- **Finding FC's window:** I made one small change to `federation-command/main.go`: FC now sets its terminal window title to "federation-command". IANAR looks for that title first. If no window has it, IANAR falls back to the window of FC's terminal process.
  - It skips processes FC shares with IANAR itself (such as local-representative), so it never types into the wrong terminal.
  - There is no reliable way to focus another app's window on GNOME Wayland. IANAR tries GNOME's `Eval` (only works in unsafe mode), then the "Window Calls" extension if installed, then robotgo on X11. If none is available, step 1 fails with a clear error.
- **Recording:** I split the screen recorder into start/stop so a run of any length can be recorded. It falls back to sampling about 4 frames a second, capped at 60s. Native Clip behaves as before. The recording's success or failure is reported separately from the run's.

Things to check on the host:
- **robotgo calls:** I couldn't look at the library source, so the `ActivePid`, `GetTitle`, `KeyTap` and `TypeStr` calls are written from memory and may not compile.
- **Steps that aren't verified:** steps 2–6 only confirm that the keys were sent, not that FC changed state. IANAR has no way to read FC's control mode.
- **No window:** if FC is running in a detached tmux session, it has no window and step 1 will fail.

The new code is in `keyboard.go`, `window.go`, `window_linux.go` and `sequence.go`. I also changed `clip.go`, `screencast_linux.go`, `remotedesktop_linux.go` and `main.go`, plus the frontend's `App.tsx`, `types.ts` and `index.css`.

## Resource 2 -- Sequence Attempt 1

Failure 1

- [Screenshot_20261004-191311.png](f4f8f159_Screenshot_20261004-191311.png)

## Revision C

This is a good start, but let's use a visual detection method to identify the window.

We see the roundabout technique is failing, and we also need direct visual inspection capability for future work.

Let's implement this now.


prompt: [`51d2d19`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/51d2d197f05aee7e7ae96c79436f7a693a899447) → reply: [`466cc33`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/466cc33e4ab72e510968810e157cf60e9c21ffd5)

## Reply C

I changed step 1 to find federation-command's window by looking at the screen, and added a reusable screen-reading feature. None of it has been built or tested: `go test`, `gofmt` and the tesseract check all needed approval, so it needs a `make build` / `make test` on the host, and `tesseract-ocr` installed there (`sudo apt install tesseract-ocr`).

- **Step 1:** IANAR takes a screenshot and reads its text with the tesseract OCR engine. It looks for a line that says exactly "federation-command" (the terminal's title bar) and clicks it to focus the window. Lines that only mention it, like IANAR's own sequence tab, don't count. If more than one line matches, it clicks the top-most.
- **What the step shows:** step 1 now includes an image of what IANAR saw. On success it's a crop with the clicked title boxed in green. On failure it's the whole screen with near-misses boxed in amber.
- **Fallback:** if the visual search fails, IANAR still tries the old window-manager methods before reporting step 1 as failed.
- **Inspect Screen:** a new control on the simple tab, with an optional "text to find" field. It shows the screenshot with every line of text it read boxed and a list of those lines, so you can check what IANAR sees.
- **Clicking:** the click goes through the same GNOME remote-desktop input as Circle Mouse, with robotgo as the fallback. GNOME's input only allows relative moves here, so IANAR first pushes the pointer into the top-left corner, then moves it to the title. Scaling is corrected using the screen size robotgo reports.

Things to check on the host:
- **Click position:** on a scaled (HiDPI) display, the click may land in the wrong place if robotgo reports the screen size wrongly.
- **Dark themes:** each screenshot is read twice, as is and with colours inverted, to help with light text on dark title bars. It's still the first thing to look at if the title isn't found.

The new code is in `ianar/vision.go` and `ianar/pointer.go`, with tests in `ianar/vision_test.go`. I also changed `window.go`, `sequence.go`, `remotedesktop_linux.go`, `main.go` and `sequence_test.go`, the frontend's `App.tsx`, `types.ts` and `index.css`, and added `tesseract-ocr` to `scripts/install-dev-deps.sh`.


## Revision D

The sequence now ran and successfully identified federation-command, but it did not succeed fully.

Let's add a capability where any of the captures and/or the recorded sequence are able to upload to file (bringing them into the preferred LR/AC cache, similar to other save-to-file functions within UFA sub-apps).

We will then use this to debug and iterate.

Let's add that now.


prompt: [`d360b41`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d360b4127d09e060f285f02cfcb7fbdea41ccf3e) → reply: [`ce6f360`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ce6f360363325ce5411a925b0ed55ce0f049ce30)

## Reply D

I added a Save to file button to IANAR. It uploads any capture, clip, screen inspection or sequence run into local-representative's files cache, the same files area agent-coordinator lists for this host. None of it has been built or tested: `go test`, `go vet`, `tsc` and `npx` all needed approval in this sandbox, so it still needs a `make build` / `make test` on the host. New tests are in `ianar/artifacts_test.go`.

- **How it works:** IANAR keeps its last 16 results, and each result message now carries an id. Save to file asks IANAR's backend to upload that result, using the same upload call the-conversationalist's Save to File makes. The browser never has to send large recordings back.
- **What gets saved:** a capture is saved as its PNG and a compositor clip as its video file. Results with several parts are saved as a `.zip`, each with a `report.txt`:
  - **Frame-sampled clip:** the frames, named by their time into the clip.
  - **Inspect Screen:** the capture, a copy with the text lines boxed, and the reading as text and JSON.
  - **Sequence run, including a failed one:** the report lists each step's status, timing, sub-actions and message. The zip also holds what each step saw (`step-N.jpg`) and the whole recording.
- **Where the button is:** on the simple tab it sits above the preview and saves whatever is showing. On the sequence-v1 tab "Save run to file" appears once a run finishes. Each button shows "saved as …" or the error, and stays disabled with a hint until IANAR is connected to local-representative.

The new backend code is in `ianar/artifacts.go`. I also changed `robot.go`, `clip.go`, `vision.go`, `sequence.go` and `main.go`, plus the frontend's `App.tsx`, `types.ts` and `index.css`.

## Resource 3 -- Sequence Failure Video

This failure occurs only when the sequence is triggered from a node OTHER THAN the one where the sequence is being executed.

- [ianar-sequence-fc-hello-world-2026-10-04T23-32-58.zip](117e9380_ianar-sequence-fc-hello-world-2026-10-04T23-32-58.zip)

## Revision E

Let's take a look at the 'Sequence Failure Video' resource for context.

Our sequence SUCCEEDS when run from the same node as the one it targets, and fails when triggered from elsewhere. (Though it always REPORTS success)

Let's determine why and fix it.

Let's also respond with whether we can inspect the included zip resource for context.


prompt: [`468022b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/468022bae13fa012b5355849f5f2478ed4f44077) → reply: [`6c4d8be`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/6c4d8bee674a75158fa1b65553c6c9f9ce93c3f6)

## Reply E

I couldn't open the zip resource or watch the failure video, so I haven't confirmed the cause. The sandbox needs approval for `unzip`, Python and `go`. All I could read were the file names: `report.txt`, `step-1.jpg` and `recording/recording.webm`.

Likely cause: from the code, a remote trigger differs from a local one only in that nobody has just used the target's desktop, so it is probably idle and GNOME has blanked or locked the screen. On a blank screen the first synthetic key only wakes it; on a locked screen the keys go to the password field instead of federation-command. It always reported success because every step only checked that a key was sent, never that federation-command reacted.

- **Step 1 now checks the screen first:** if it's locked, the run fails with a clear error. If it has blanked, IANAR wakes it and waits before looking for the window. This is in the new `ianar/screenstate.go` and `screenstate_linux.go`.
- **Step 5 now checks the command ran:** it counts the "hello world!" output lines on screen before and after Enter. If no new one appears, the step fails with a screenshot. This is in `ianar/sequence.go`. It could wrongly fail if an older output line scrolls off screen at the same moment the new one appears.

New tests are in `ianar/screenstate_test.go` and `ianar/sequence_test.go`, but I couldn't run them or `gofmt`, so it needs a `make build` / `make test` on the host. If you approve `unzip` (or extract the zip for me), I can read `report.txt` and `step-1.jpg` to check the diagnosis. The video I can't view either way.


## Revision F

Let's improve the condoccer 'Add Resources' behaviour.

Now whenever a zip file is included in a resource it will also be extracted to a folder of the same name beside the file (minus the zip extension).


prompt: [`2696b00`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/2696b009b271abe2eaf6a25e7c76681dc8dfef38) → reply: [`3ed82c7`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3ed82c71b2edc7eff4e98eab38b53089ea5ede77)

## Reply F

I added zip extraction to condoccer's Add Resources. I couldn't build or test it, because `go test`, `go vet` and `gofmt` all needed approval in this sandbox. It needs a `make build` / `make test` on the host. Any `.zip` added through **Highlighted** or **Upload** is now unpacked into a folder with the same name, minus the `.zip`, beside the file in the Impls folder (`abcd1234_run.zip` → `abcd1234_run/`). The zip itself is kept. If a zip can't be unpacked, the action fails with an error and no resource block is added. The changes are in `condoccer/resources.go`, with new tests in `condoccer/resources_test.go`.

**Extraction guards:**
- Entries that would land outside the folder are rejected, and so are symlinks.
- Unpacked contents are capped at 1 GiB in total, so a zip bomb can't fill the disk.
- If unpacking fails partway, the half-made folder is removed and only the zip stays.

Zips already added, such as the sequence failure zip from Revision E, are not unpacked, because I didn't touch condoc files. They'd need to be added again or unzipped once by hand.


## Revision G

Let's do another iteration - 'sequence-v2'

This iteration we will have three sub-tabs under the new sequence-v2 tab: the definer, the composer, and the runner.

The definer allows pre-defined actions to be created which expose controls. The composer allows steps to be dragged-and-dropped to create sequences.

Sequences will always be created in the composer with steps defined in the definer. Both the definer and composer constructs may be imported or exported as yaml.

For this new system we will implement the same sequence we had in V1, we will also add sequences which: 
- opens a new in-private firefox window via the icon, navigates to a weather page, and prints out what the temperature is in Portugal (the country is a control and may be varied).
- creates a new text document on the desktop, opens the text document, writes hello world in it, saves the screenshot to the desktop, and deletes the text file

We will be able to run any of these three example sequences from the runner tab, we will also be able to re-use the building blocks to create new sequences.


prompt: [`805c2d5`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/805c2d551eee5a0ceccd4fae84b270a319919d48) → reply: [`9146e52`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/9146e52329cd6e69135e6135cc5fbb1986c485b6)

## Reply G

I added the **sequence-v2** tab to IANAR, with **definer**, **composer** and **runner** sub-tabs. The runner has three ready-made sequences: v1's federation-command "hello world", a Firefox private-window temperature check with the country as a control (Portugal by default), and the desktop text-file sequence. None of it has been built, tested or tried on screen: `go`, `gofmt`, `tsc` and fetching Go module checksums all needed approval in this sandbox, so it needs a `make build` / `make test` on the host. The tests are in `ianar/seqv2_test.go`.

- **Definer:** you build reusable actions from about 15 basic operations: press keys, type, find and click text or an app's icon on screen, wait for text, read a value off the screen, print, and create, check or delete a file. Each action exposes controls with defaults, which instructions use as `{{name}}`.
- **Composer:** you drag actions into a sequence and fill in their controls. Dragging also works on a phone, and each action has a **+** button too.
- **Runner:** shows each step's status, prints any values the sequence reads (such as the temperature), records the run, and offers Save to file.
- **YAML:** actions and sequences import and export as YAML, and an exported sequence includes the actions it uses. I wrote a small YAML reader/writer (`yamlite.go`) instead of adding a library, because I couldn't fetch the checksums `go.sum` would need for a new module.
- **Where it's saved:** the library is kept in `~/.config/ianar/sequence-v2.yaml`. You can change that with `--sequence-library`, and a "Restore examples" button puts the built-in sequences back.

How the two new sequences work, and what to check on the host:
- **Firefox weather:** it finds Firefox's dock icon by matching its picture (new code in `icon.go`), right-clicks it and picks "New Private Window". It then opens `wttr.in`'s one-line weather page, which OCR reads reliably, and prints the temperature. The icon matching is new and only checked against a synthetic test image; it also fails if the dock is hidden.
- **Desktop text file:** IANAR creates the file directly rather than through the desktop. It opens the file through the Alt+F2 Run dialog so the editor comes to the front, then types, saves, checks the file really contains "hello world", saves a screenshot to the desktop, closes the editor and deletes the file. The keys assume an editor that saves on Ctrl+S and closes on Ctrl+W, such as GNOME Text Editor.

I also added right-click and double-click (in `pointer.go` and `remotedesktop_linux.go`) and more key names (F1–F12, Super, Delete, Page Up/Down) in `keyboard.go`. On the frontend, the preview and Save to file pieces moved from `App.tsx` into `shared.tsx`; the new tab is in `SequenceV2.tsx`.

## Resource 4 -- Debug Sequences

The new sequence-v2 sequences are on the right track but each has problems.

- [ianar-sequence-firefox-weather-2026-10-05T19-45-58.zip](4bebe3c2_ianar-sequence-firefox-weather-2026-10-05T19-45-58.zip)
- [ianar-sequence-desktop-text-file-2026-10-05T19-44-08.zip](d48f3d19_ianar-sequence-desktop-text-file-2026-10-05T19-44-08.zip)
- [ianar-sequence-fc-hello-world-2026-10-05T19-41-12.zip](98fc772d_ianar-sequence-fc-hello-world-2026-10-05T19-41-12.zip)

## Revision H

The new sequences have some bugs - let's view the resource 'Debug Sequences' for details and use the feedback to debug the sequences.

Let's fix the errors now.


prompt: [`cb07f4f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/cb07f4f8e7f2ff1358ce9a525e4d58e08d53f2b2) → reply: [`e7efd2a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/e7efd2a79c7b5d013628ddc11cab7e45af0bfcf5)

## Reply H

I fixed both causes behind the three failures, but none of it has been built or tested. `gofmt`, `go vet` and `go test` all needed approval in this sandbox, so it still needs a `make build` / `make test` on the host. Revision F's unpacking worked, so I could read every `report.txt` and step image this time. I couldn't watch the `recording.webm` videos: `ffmpeg` and Python need approval too.

**Main cause: clicks landed one monitor's width to the right.** IANAR moved the pointer to the top-left corner with one huge move before each click. GNOME stops a move that would leave the screen at the edge of the monitor the pointer is on. So on your two monitors, starting from the right-hand one, the pointer stopped at (1920, 0) instead of (0, 0).
- **Firefox:** the right-click meant for its dock icon hit the right monitor's desktop and opened the desktop menu instead.
- **federation-command:** the click on its title bar was pushed off the right edge of the screen, so the window never got focus and the typed command never reached it.
- **The fix:** IANAR now moves the pointer up and left in small alternating steps, so it crosses from one monitor to the next and reaches the real corner. This is `homePointer` in `ianar/pointer.go`. This probably also explains Revision E's "only fails from another node": on the local node, the pointer was usually already on the left monitor.

**Text-file run: a leftover menu took the keys.** The desktop menu from an earlier Firefox attempt was still open, and it was still open when step 3 failed. That suggests Alt+F2 and the typed `xdg-open` command went into the menu rather than opening the Run dialog. I'm inferring that from the screenshot, since I couldn't see the video.
- **Escape first:** the "open file" and "open Firefox private window" actions now press Escape before doing anything else, to close any leftover menu.
- **Ignoring the command line:** the check that the file opened now skips the Run dialog's own `xdg-open …` line, so that line can't be mistaken for the file opening.

Your saved library in `~/.config/ianar/sequence-v2.yaml` still has the old versions of those two actions, so press **Restore examples** to get the new ones. The pointer fix needs nothing extra.

I updated the existing tests in `vision_test.go` and `seqv2_test.go`, and added `TestClickAtAcrossMonitors`. It mimics GNOME's edge behaviour on two side-by-side monitors and checks that clicks starting from the right monitor land on the right spot on both. The other changes are in `ianar/pointer.go` and `ianar/seqv2_examples.go`.

## Resource 5 -- Debug Sequences 2

Improved but not fixed

- [ianar-sequence-desktop-text-file-2026-10-05T20-05-45.zip](cdc6a0c2_ianar-sequence-desktop-text-file-2026-10-05T20-05-45.zip)
- [ianar-sequence-firefox-weather-2026-10-05T20-04-27.zip](acc4ba56_ianar-sequence-firefox-weather-2026-10-05T20-04-27.zip)

## Revision I

Our last iteration improved things but did not fix everything.

Let's view 'Debug Sequences 2' resource for context and attempt the remainder of the fixes.

Let's fix these now.


prompt: [`6741469`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/6741469919baf18d84446d55155b7b73772e3ccd) → reply: [`1678680`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/1678680eced2f2ff0054f9e0a2bbc6d00402b9a5)

## Reply I

I've made fixes for both remaining failures, but nothing has been built, tested or tried on screen. `gofmt`, `go vet`, `go test` and `ffmpeg` all needed approval here, so I couldn't watch the recordings and worked only from the two reports and step images. It needs a `make build` / `make test` on the host. After that, press **Restore examples**: your saved library still has the old versions of the two changed actions.

- **Firefox: missed menu item.** The right-click worked and the step image shows "New Private Window" in the menu, but the text reader never picked it up. On your two monitors the screenshot is 3840 px wide, which skipped the 2x enlargement IANAR uses so small text can be read. It now decides by height instead, so your setup gets 2x again. Matching also now allows about one misread letter per 10 in names of 8 letters or more. The final "Private" check now waits for a *new* line, because IANAR's own step list already contains that word.
- **Text file: Alt+F2 doesn't work.** No menu was open this time and still nothing opened, so the Run dialog approach fails on its own. IANAR now opens the file directly with `gio open` (falling back to `xdg-open`). GNOME doesn't give focus to a window opened that way, so IANAR waits for a new line reading exactly `ianar-hello-world.txt` (the editor's title) and clicks it. It ignores lines that were already on screen before opening, such as the file's icon label on the desktop.

To support this I added an `open` step, a "count only exact matches" option on `count-text`, and a `new_since` option on `click-text`. The changes are in `ianar/vision.go`, `ianar/seqv2_ops.go`, `ianar/seqv2_examples.go` and `ianar/sequence.go`. I updated the Firefox and text-file tests in `seqv2_test.go`, and added tests for the new matching and enlargement rule in `vision_test.go`.

Reading screenshots at 2x on two monitors will make each look at the screen slower. The title click assumes the editor shows just the file name, as GNOME Text Editor does. An editor like gedit, whose title adds more text, won't match.


## <REPLACE-Revision|Retry> J

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
