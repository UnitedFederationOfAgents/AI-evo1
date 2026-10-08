# InitialRobot

<!--
```condoc-yaml
condoc:
  startTime: 1790974611
  controlScheme: same-repo
  branch: condoc/InitialRobot-1790974611/main
  callerPath: ..
```
-->

Create the initial baseline for I am Not a Robot.


### Step 1 - Create the shell of I am Not a Robot (IANAR).

[Step 1 Prompt](initialRobotImpls/Step1Prompt.md)

```prompt
I am Not a Robot (IANAR) is another go-with-frontend sub-app which is a 'representable' hosted UI, under a tab 'robot'.

It has channels to collect and drive native capture and input, as well as browser based.

In the first increment the shell will be created, along with all of the standard hookups (makefiles, gitignores, etc).

It will also be given a:
- capture native button, which captures the full native display
- capture browser button, which captures from the browser
- circle mouse button, which when clicked uses native control to make the mouse do a medium sized circle, going clockwise, taking about 1 second
```


### Step 2 - Add the 'Native Clip' control to IANAR

[Step 2 Prompt](initialRobotImpls/Step2Prompt.md)

```prompt
Now that we have our first few working controls we will add 'Native Clip'.

The Native Clip button will capture a 4 second video clip of the desktop and present it for playback.
```


### Step 3 - Add a 'control' tab to LR and AC

[Step 3 Prompt](initialRobotImpls/Step3Prompt.md)

```prompt
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
```


### Step 4 - Improve the recording capabilities for our control sequences.

[Step 4 Prompt](initialRobotImpls/Step4Prompt.md)

```prompt
Now that we have control sequences working nicely we will make the recording capabilities more flexible.

In the composer, to the right of the main 'steps' blocks we will allow much slimmer 'recording' blocks to be created. These blocks may be longer than a single step (one block may span N steps). They may overlap one-another, and there may be up to 3 in parallel.

Each one of these blocks represents a recording being taken on one node involved in the sequence. The same node may not be in two blocks which are side-by-side for any particular step.

These recordings are gathered in the sequence collection.

This will allow us to do things like only record important segments, and to record sequences which are in differing locations.

Let's begin this implementation now.
```


## Human-Prompt

The flow of the condoc is now within the fourth step.
