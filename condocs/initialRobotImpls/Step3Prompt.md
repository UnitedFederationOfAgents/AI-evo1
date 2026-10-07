# Prompt

[InitialRobot](../InitialRobot.md)

Next we will add a 'control' tab to LR and AC.

This tab is used for sequencing various actions across all sub-apps.

Everything we create under control will be nested under a 'v1' sub-tab for now.

We will create a sample implementation which:
- Launches a new FC instance (directing the commands found on the system tab)
- Uses the channel to FC to echo "This is the one - <random-chars>" through the remote interface
- Uses the robot to find that FC terminal and bring it to local control (like our robot sequence)
- Uses the robot to type into FC and echo "found it"
- Puts the terminal back into remote control

Let's also make the federation-command tab able to recognize and handle the multiple FC instances now, while we are at it.


prompt: [`f1f9ac1`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/f1f9ac16e32a787947cd832d5a750d29301a4d47) → reply: [`493db3b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/493db3b29b65e08d92141714e1979cc35c4401c8)

## Reply

I added a `control` tab with a `v1` sub-tab to both LR and AC, plus the sample sequence: it launches a new FC instance from the system tab, echoes `This is the one - <random>` through it, has the robot find that terminal and take local control, type `echo "found it"`, and hand it back to remote control. LR checks each step from what that instance reports back (its output and control state), and saves the robot's recordings to the files tab. The federation-command tab in LR and AC now lists every FC instance with a picker and per-instance logs and commands, because each FC now connects to LR under its own name (`federation-command#N`); none of this has been built or tested, since `go`, `gofmt` and `tsc` all needed approval here, so it needs a `make build` / `make test` on the host, where new tests are in LR, ianar and AC (`fcinstances_test.go`, `control_test.go`, `lrrun_test.go`). The robot can only find the new FC terminal if its window is visible on screen and no other view of FC's output (for example LR's own federation-command tab) is showing the marker line on that desktop.

## Resource 1 -- Failure Mode 1

Robot doesn't find window.

- [ianar-capture-native-2026-10-06T11-05-21.png](bc8af427_ianar-capture-native-2026-10-06T11-05-21.png)

## Resource 2 -- Failure Mode 1 Controlled Node

The node we are controlling on

- [ianar-capture-native-2026-10-06T11-05-51.png](84a734c9_ianar-capture-native-2026-10-06T11-05-51.png)

## Revision A

Let's look at the 'Failure Mode 1' resources.

Our sample sequence almost worked but the robot handoff didn't go quite right.

Let's fix this failure mode.

We also encountered a failure mode where another FC instance was already running - not launched from LR controls, launched through the auto-launch config. In that failure mode we did not have the text flow through the channel of the newly created FC instance and the robot had nothing to find.

Let's fix this failure mode as well.

Let's also instrument the sequence with the recording of a video on the targeted node. This is not a mandatory thing which will always happen, but in this case we will add it to this sequence. (In some later cases we may not record, or we may record multiple nodes, etc)


prompt: [`a2bfa2a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/a2bfa2af59837200369cb583a2bb4b4415edcbf2) → reply: [`b09c4a3`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b09c4a3766987bda474010c7804927d55c30de78)

## Reply A

I fixed both failure modes and added a screen recording of the robot's machine for the whole sequence; none of it is built or tested yet, because `go` needed approval here. The first failure came from the screen reader dropping the lone "-" in "This is the one - vh4uyx", which split the line in two so it never matched; it now also tries joining pieces of the same line that sit a few characters apart. For the second, a newly launched FC can no longer reuse the name of one still connected, and the sequence now stops with a clear error if the new FC didn't connect under its own name or the marker came back from a different FC. The recording runs from before the first step to after the last, even on failure, and saves a video to the files tab, with the robot's own runs leaving out their separate recordings. Each sequence chooses whether to record. New tests are in `ianar/vision_test.go`, `ianar/lrrecord_test.go`, `local-representative/control_test.go` and `fcinstances_test.go`, so this needs a `make build` / `make test` on the host.


## Revision B

Let's add a few more minor features here:
- Let's make the recordings available to play back in the same view where we see the sequence executing
- Let's make the sequence begin by unlocking the screen if it is locked


prompt: [`d54b7d0`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d54b7d0530cec970b7b012780d20f22e77a825e1) → reply: [`9d13034`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/9d1303424f451f20cbad856c27fa85cbdcf70a28)

## Reply B

I added both features, but none of it has been built or tested yet: `go` and `tsc` needed approval here, so it needs a `make build` / `make test` on the host. After a run, the control view (in both LR and AC) now shows the saved recording as a video player under the steps, with a download link. If only sampled frames could be saved, it shows just a link to the `.zip`. The sequence now starts with an "Unlock the screen if it is locked" step, which runs before the recording starts. It uses a new robot instruction, `unlock-screen`, that asks the session manager to unlock the user's own session (the same as `loginctl unlock-session`, no password typed) and then wakes the screen. That only works if the system's default permissions still allow a user to unlock their own session; if not, the step stops with the reason. New tests are in `ianar/screenstate_test.go` and `local-representative/control_test.go`.


## Substep C - Take a detour to fix a sessions bug

[Step 3 Substep C](Step3SubstepCPrompt.md)


## Revision C

Now let's extend the 'control' functionality to make it so that we can define/compose/run these sequences. Similar to what we did for the robot.

We will keep this existing sequence as an example.

We will use a yaml notation to allow importing/exporting of sequences.


prompt: [`0e04e0e`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0e04e0e26a37a4a1b5d3b0223976b5835a8d27f9) → reply: [`3aabbf4`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3aabbf414111661c0dc824e0ff1a19ed78dbe290)

## Reply C

You've hit your session limit · resets 2:30pm (UTC)


## Revision D

Now let's extend the 'control' functionality to make it so that we can define/compose/run these sequences. Similar to what we did for the robot.

We will keep this existing sequence as an example.

We will use a yaml notation to allow importing/exporting of sequences.


prompt: [`2bdc69b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/2bdc69bafcfef29259d5d060acf468ca7b6a5d69) → reply: [`16a6aed`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/16a6aedf74c5b0d9d9faf5be42807f110baa7066)

## Reply D

I've finished Revision D: control sequences are now data that you can define, compose, run and import/export as YAML, the same way the robot's sequence-v2 tab works. Nothing has been built or tested, because `go`, `gofmt` and `tsc` all needed approval here. It needs a `make build` / `make test` on the host.

- **Where it is:** the control tab's v1 sub-tab now has three views, in both LR and AC:
  - **runner:** pick a sequence, fill in its controls, and run it.
  - **definer:** build actions from LR's own steps (launch FC, send a command, wait for output or control state, and so on) or `robot.<op>` steps. The robot now sends LR its list of ops when it connects.
  - **composer:** put actions in order, set their values, choose whether to record the screen, and mark which leading steps run before recording.
- **The example:** the existing hand-off sequence is kept as the built-in example, `fc-robot-handoff`, rebuilt from six actions. A "restore examples" button puts it back.
- **Saving and import/export:** the library is saved to `~/.config/local-representative/control-v1.yaml` (`--control-library`). Imports and exports use the `lr-control-v1` YAML format, and an exported sequence carries the actions it uses.
- **Size limit through AC:** the LR–AC link drops a connection on any message over 64 KB. So AC refuses requests over 48 KB, and LR trims (or leaves out) the library it sends AC if it's too large; in both cases the page says to use the host's own control tab.

I continued from the files the interrupted Revision C attempt had left in LR, and added the engine changes, the AC and robot wiring, and one shared `ControlV1.tsx` that both frontends use. New tests are in `local-representative/controllib_test.go`, with updates to `control_test.go`, and in `ianar/repr_test.go`.


## Revision E

We have a build error in this increment:

=== Building local-representative ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/local-representative'
cd frontend && npm install && npm run build

up to date, audited 69 packages in 1s

7 packages are looking for funding
  run `npm fund` for details

3 vulnerabilities (1 moderate, 2 high)

To address issues that do not require attention, run:
  npm audit fix

To address all issues (including breaking changes), run:
  npm audit fix --force

Run `npm audit` for details.

> local-representative-frontend@1.0.0 build
> tsc && vite build

The CJS build of Vite's Node API is deprecated. See https://vite.dev/guide/troubleshooting.html#vite-cjs-node-api-deprecated for more details.
vite v5.4.21 building for production...
transforming...
✓ 32 modules transformed.
rendering chunks...
computing gzip size...
dist/index.html                   0.41 kB │ gzip:  0.27 kB
dist/assets/index-BnZyqK35.css   30.72 kB │ gzip:  5.75 kB
dist/assets/index-DVMUXoqh.js   210.23 kB │ gzip: 64.42 kB
✓ built in 2.17s
go build -ldflags "-X ufa-version.Version=v0.0.3-inirob-b95e59d" -o local-representative .
# local-representative
./controlops.go:151:5: initialization cycle for controlOps
	./controlops.go:151:5: controlOps refers to opLaunchFC
	./controlops.go:254:6: opLaunchFC refers to addValue
	./control.go:546:25: addValue refers to broadcast
	./control.go:285:25: broadcast refers to state
	./control.go:271:25: state refers to sequenceInfos
	./control.go:250:25: sequenceInfos refers to snapshot
	./controllib.go:1121:26: snapshot refers to controlOps
make[1]: *** [Makefile:20: build-go] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/local-representative'

=== Build failed -- leaving /AI-evo1-dev/bin untouched ===
make: *** [Makefile:77: deploy-dev-binaries] Error 1

Let's fix that.


prompt: [`b95e59d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b95e59d74247fb94b552c9b1bc4a5188096d3eed) → reply: [`c15c0e6`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/c15c0e6ac64c2ba236c39cff82987afc653b8276)

## Reply E

I fixed the build error by breaking the initialization cycle in `local-representative/controlops.go`. The op table literal is now a separate var, `lrControlOps`. `controlOps` is declared without an initializer and set from `lrControlOps` in an `init()`. Go's initialization-cycle check only applies to variable initializers, so the loop back to `controlOps` (`opLaunchFC` → `broadcast` → `snapshot` → `controlOps`) no longer counts as a cycle. Nothing else in the code changes.

I haven't built or tested it, because `go`/`gofmt` needed approval here. Please re-run `make build` / `make test` on the host to confirm.


## Revision F

Let's add a second example sequence where we use the robot to take native captures from two nodes and then upload them to 'files' as screenshots.

The sequence will expose inputs which allow the selection of nodes for this action - the node selections are constrained to ones which are presently connected to AC.


prompt: [`c9e76bb`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/c9e76bbaed68fd27388e0e66ae0d98e78788351b) → reply: [`c3f18e2`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/c3f18e2d95ed3301d80805d8a739e256485855e4)

## Reply F

I added a second built-in control sequence, `capture-two-nodes` ("Robots: screenshot two nodes"), but none of it has been built or tested. `go` and `tsc` needed approval here, so it needs a `make build` / `make test` on the host.

- **Choosing nodes:** the sequence has two controls of a new `node` type. In the runner they are drop-downs that list only the nodes connected to AC. AC tells every LR which nodes are connected whenever one comes or goes, and a run won't start if a control names a node that isn't connected.
- **Taking the screenshots:** a new `node-capture` op has a node's robot take a native capture and save it as `ianar-capture-native-<node>-<time>.png`. For another node, the request goes through AC to that node's LR and robot, and the screenshot is copied back through AC's read-only `/host/<node>/*` proxy. For this node, its own robot is used directly.
- **Where they end up:** both screenshots land in the files tab of the LR that ran the sequence, and the control view shows them as images under the steps, in LR and AC. Another node's screenshot also stays in that node's own files tab.
- **Existing libraries:** a library saved before this change picks up the new example when LR starts. An example you deleted stays deleted.

One behaviour to know: a run fails at a node whose robot isn't running, with the reason shown. It doesn't unlock a locked screen first, so a locked node gives a screenshot of its lock screen.

The main new code is in `local-representative/controlnodes.go`, `agent-coordinator/controlnodes.go` and `ianar/lrcapture.go`, and the shared `ControlV1.tsx` / `controlTypes.ts` are copied to AC. New tests are `controlnodes_test.go` in LR and AC and `ianar/lrcapture_test.go`, plus additions to `controllib_test.go`. I also updated the existing tests that counted one example sequence, and the control-tab section of the LR README.


## Revision G

Let's add a third example sequence - "you tell me" - in this sequence we begin the same way as the robot handoff, but the phrase we first echo in the terminal is "<random chars> Enter a phrase to capture!" in remote mode.

Then we switch to local mode, and we type 'echo ""' and leave it on the commandline.

Then in the sequence dialog we have a step explaining to the user that they should enter text and enter the command and then press continue. The sequence dialog presentation has a 'continue' button.

When the user presses continue the sequence collects the phrase that the user has echoed and brings it back to the output of the sequence dialog.

Let's implement this now.
