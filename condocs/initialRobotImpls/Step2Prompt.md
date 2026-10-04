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


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
