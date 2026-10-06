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
