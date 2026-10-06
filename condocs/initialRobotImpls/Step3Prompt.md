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


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
