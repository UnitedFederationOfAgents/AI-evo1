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


## <REPLACE-Revision|Retry> C

<REPLACE-PROMPT>


## Human-Prompt

Add the '!HANDOFF!' or '!COMPLETED!' directive.
