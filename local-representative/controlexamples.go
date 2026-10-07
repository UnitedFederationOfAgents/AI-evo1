package main

// This file is the control library LR starts with (and restores on "Restore
// examples"): the sample sequence the control tab was first built around
// (condocs/initialRobotImpls/Step3Prompt.md, with Revisions A and B), kept
// as an example now that sequences are data (Revision C), and the actions
// it is composed of:
//
//   - unlock the robot's screen if it is locked (before the recording)
//   - launch a new federation-command instance
//   - echo "This is the one - <random-chars>" through the remote interface
//   - have the robot find that terminal by the marker and take local control
//   - have the robot type into it: echo "found it", into a new file on the
//     desktop (Revision I)
//   - put the terminal back into remote control
//   - upload that file to the files tab with node-fetch-file
//   - delete the file, through the remote interface
//
// with this node's screen recorded across the run. node-fetch-file only
// takes the file if this node's control-fetch-allow setting allows it
// (~/Desktop/found-it-*.txt, say).
//
// And a second (Revision F), capture-two-nodes: have the robots of two nodes
// connected to agent-coordinator, chosen in the runner, each take a native
// capture, saved as screenshots in the files tab (see controlnodes.go).
//
// And a third (Revision G), you-tell-me: begin as the hand-off does, echoing
// "<random-chars> Enter a phrase to capture!" in remote control; have the
// robot take local control and leave echo "" on the command line; then wait
// for the person running it to echo a phrase there and press continue, and
// bring that phrase back as the run's output. The terminal is left in local
// control: by then the keyboard focus is on the browser, so the robot's ←
// wouldn't reach it.

func exampleControlLibrary() ([]ControlActionDef, []ControlSequenceDef) {
	fcControl := ControlParam{Name: "instance", Label: "federation-command instance", Default: "{{fc}}", Help: "the instance, as launch-fc saved it"}
	actions := []ControlActionDef{
		{
			ID: "unlock-screen", Name: "Unlock the screen if it is locked",
			Description: "robot: unlock this node's screen through logind if it is locked, and wake it if it has blanked",
			Do:          []ControlInstruction{{"op": "robot.unlock-screen"}},
		},
		{
			ID: "launch-fc", Name: "Launch a new federation-command instance",
			Description: "launch federation-command as the system tab does, then wait for the new instance to connect under its own name, in remote control",
			Controls:    []ControlParam{{Name: "save_as", Label: "Save as", Default: "fc", Help: "the name later steps refer to the instance by"}},
			Do:          []ControlInstruction{{"op": "launch-fc", "save_as": "{{save_as}}"}},
		},
		{
			ID: "echo-marker", Name: "Echo a marker through the remote interface",
			Description: "send an echo of a marker ending in random characters to that instance only, and wait for its output to come back -- from it and no other instance; saved as {{marker}}",
			Controls: []ControlParam{
				fcControl,
				{Name: "prefix", Label: "Marker prefix", Default: "This is the one", Help: "the marker is this, \" - \" and random characters"},
			},
			Do: []ControlInstruction{
				{"op": "random", "save_as": "marker_token", "length": "6"},
				{"op": "set", "save_as": "marker", "value": "{{prefix}} - {{marker_token}}"},
				// Shown as the whole command, never the bare marker: the robot
				// clicks the line reading exactly the marker, and that must be
				// FC's output, not this run's own display of it.
				{"op": "show", "label": "marker command", "value": `echo "{{marker}}"`},
				{"op": "fc-send", "fc": "{{instance}}", "command": `echo "{{marker}}"`, "expect": "{{marker}}"},
			},
		},
		{
			ID: "robot-take-local", Name: "Find a terminal with the robot and take local control",
			Description: "robot: click the line reading the text on screen, press →; then check the instance reports local control",
			Controls: []ControlParam{
				fcControl,
				{Name: "text", Label: "Text to find", Default: "{{marker}}", Help: "a line only that instance's terminal shows"},
			},
			Do: []ControlInstruction{
				{"op": "fc-require-window", "fc": "{{instance}}"},
				{"op": "robot.ensure-awake"},
				{"op": "robot.click-text", "text": "{{text}}", "exact": "true", "pick": "bottom", "timeout": "10s"},
				{"op": "robot.key", "keys": "right"},
				{"op": "fc-expect-state", "fc": "{{instance}}", "state": "local-control", "hint": "the robot pressed → but the click may have focused another window"},
			},
		},
		{
			ID: "robot-type-command", Name: "Type a command into a terminal with the robot",
			Description: "robot: clear the command line, type the command, press Enter; then check the instance printed the expected line",
			Controls: []ControlParam{
				fcControl,
				{Name: "command", Label: "Command", Default: `echo "found it"`},
				{Name: "expect", Label: "Expected output", Default: "found it", Help: "a line the command prints"},
			},
			Do: []ControlInstruction{
				{"op": "robot.key", "keys": "end ctrl+u"},
				{"op": "robot.type", "text": "{{command}}"},
				{"op": "robot.key", "keys": "enter"},
				{"op": "fc-expect-output", "fc": "{{instance}}", "text": "{{expect}}", "hint": "the robot typed the command, but its output didn't come back"},
			},
		},
		{
			ID: "robot-give-back", Name: "Put a terminal back into remote control",
			Description: "robot: press ←; then check the instance reports remote control",
			Controls:    []ControlParam{fcControl},
			Do: []ControlInstruction{
				{"op": "robot.key", "keys": "left"},
				{"op": "fc-expect-state", "fc": "{{instance}}", "state": "remote-control", "hint": "the robot pressed ← but the terminal stayed in local control"},
			},
		},
		{
			ID: "fetch-node-file", Name: "Upload a file from a node to the files tab",
			Description: "node-fetch-file: copy a file from a node into this node's files tab, if that node's control-fetch-allow setting allows it",
			Controls: []ControlParam{
				{Name: "node", Label: "Node", Type: controlTypeNode, Default: "{{this_node}}", Help: "the node the file is on"},
				{Name: "path", Label: "File", Help: "an absolute path or glob; ~ is the node user's home"},
			},
			Do: []ControlInstruction{
				{"op": "node-fetch-file", "node": "{{node}}", "path": "{{path}}", "hint": "the node's control-fetch-allow setting has to allow the file"},
			},
		},
		{
			ID: "fc-remove-file", Name: "Delete a file through the remote interface",
			Description: "send rm for the file to that instance, and wait for it to report rm's exit status -- failing unless it's 0",
			Controls: []ControlParam{
				fcControl,
				{Name: "path", Label: "File", Help: "the file to delete, as the shell takes it (~ is expanded; no spaces)"},
			},
			Do: []ControlInstruction{
				{"op": "random", "save_as": "rm_token", "length": "6"},
				{"op": "fc-send", "fc": "{{instance}}", "command": `rm -- {{path}}; echo "{{rm_token}} exit=$?"`},
				{"op": "fc-expect-output", "fc": "{{instance}}", "text": "{{rm_token}} exit=0", "fail_text": "{{rm_token}} exit=", "hint": "rm couldn't delete the file"},
			},
		},
		{
			ID: "node-screenshot", Name: "Screenshot a node with its robot",
			Description: "robot: take a native capture of a node's screen and save it as a screenshot in the files tab (another node's through agent-coordinator, copied here)",
			Controls:    []ControlParam{{Name: "node", Label: "Node", Type: controlTypeNode, Help: "a node connected to agent-coordinator"}},
			Do:          []ControlInstruction{{"op": "node-capture", "node": "{{node}}"}},
		},
		{
			ID: "echo-prompt-marker", Name: "Echo a prompt marker through the remote interface",
			Description: "send an echo of random characters followed by a prompt to that instance only, and wait for its output to come back -- from it and no other instance; saved as {{marker}}",
			Controls: []ControlParam{
				fcControl,
				{Name: "text", Label: "Prompt", Default: "Enter a phrase to capture!", Help: "the marker is random characters, a space and this"},
			},
			Do: []ControlInstruction{
				{"op": "random", "save_as": "marker_token", "length": "6"},
				{"op": "set", "save_as": "marker", "value": "{{marker_token}} {{text}}"},
				{"op": "show", "label": "marker command", "value": `echo "{{marker}}"`},
				{"op": "fc-send", "fc": "{{instance}}", "command": `echo "{{marker}}"`, "expect": "{{marker}}"},
			},
		},
		{
			ID: "robot-type-leave", Name: "Type onto a terminal's command line with the robot, without running it",
			Description: "robot: clear the command line, type the text and press the keys after it, leaving it for someone to finish; then check the instance is still in local control",
			Controls: []ControlParam{
				fcControl,
				{Name: "text", Label: "Text", Default: `echo ""`},
				{Name: "keys", Label: "Keys after it", Default: "left", Help: "pressed after typing; ← puts the cursor between the quotes"},
			},
			Do: []ControlInstruction{
				{"op": "robot.key", "keys": "end ctrl+u"},
				{"op": "robot.type", "text": "{{text}}"},
				{"op": "robot.key", "keys": "{{keys}}"},
				{"op": "fc-expect-state", "fc": "{{instance}}", "state": "local-control", "hint": "the robot's keys left local control"},
			},
		},
		{
			ID: "capture-user-echo", Name: "Wait for someone to echo a phrase, then capture it",
			Description: "show a message with a continue button; once it's pressed, take the phrase the last echo typed into the instance printed, as the run's output",
			Controls: []ControlParam{
				fcControl,
				{Name: "message", Label: "Message", Default: `Type a phrase between the quotes of the echo "" the robot left on federation-command's command line, press Enter there, then press continue here.`},
			},
			Do: []ControlInstruction{
				{"op": "ask-user", "message": "{{message}}"},
				{"op": "fc-capture-echo", "fc": "{{instance}}", "save_as": "phrase", "hint": "type the phrase into the terminal and press Enter there before pressing continue"},
				{"op": "output", "label": "captured phrase", "value": "{{phrase}}"},
			},
		},
	}
	sequences := []ControlSequenceDef{
		{
			ID:          "fc-robot-handoff",
			Name:        "federation-command: robot hand-off",
			Description: "Launch a new federation-command, mark it over the remote interface, then have the robot find it, take local control, echo \"found it\" into a new file on the desktop and hand it back; then upload that file to the files tab and delete it.",
			Record:      []string{recordLocalRobot},
			Controls: []ControlParam{
				{Name: "found_file", Label: "Desktop file", Default: "~/Desktop/found-it-{{timestamp}}.txt", Help: "the new file \"found it\" is echoed into (~ is expanded; no spaces) -- this node's control-fetch-allow must allow it, e.g. ~/Desktop/found-it-*.txt"},
			},
			Steps: []ControlStepRef{
				{Action: "unlock-screen", BeforeRecording: true},
				{Action: "launch-fc"},
				{Action: "echo-marker", Label: `Echo "This is the one - <random-chars>" through the remote interface`},
				{Action: "robot-take-local", Label: "Find that terminal with the robot and bring it to local control"},
				{Action: "robot-type-command", Label: `Type into it with the robot: echo "found it" into a new file on the desktop`,
					With: map[string]string{"command": `echo "found it" | tee {{found_file}}`}},
				{Action: "robot-give-back", Label: "Put the terminal back into remote control"},
				{Action: "fetch-node-file", Label: "Upload that file to the files tab", With: map[string]string{"node": "{{this_node}}", "path": "{{found_file}}"}},
				{Action: "fc-remove-file", Label: "Delete the file", With: map[string]string{"path": "{{found_file}}"}},
			},
		},
		{
			ID:          "capture-two-nodes",
			Name:        "Robots: screenshot two nodes",
			Description: "Have the robots of two nodes connected to agent-coordinator each take a native capture, saved as screenshots in this node's files tab.",
			Controls: []ControlParam{
				{Name: "first_node", Label: "First node", Type: controlTypeNode, Help: "a node connected to agent-coordinator"},
				{Name: "second_node", Label: "Second node", Type: controlTypeNode, Help: "a node connected to agent-coordinator"},
			},
			Steps: []ControlStepRef{
				{Action: "node-screenshot", Label: "Screenshot the first node with its robot", With: map[string]string{"node": "{{first_node}}"}},
				{Action: "node-screenshot", Label: "Screenshot the second node with its robot", With: map[string]string{"node": "{{second_node}}"}},
			},
		},
		{
			ID:          "you-tell-me",
			Name:        "federation-command: you tell me",
			Description: "Launch a new federation-command and mark it over the remote interface, have the robot take local control and leave echo \"\" on its command line, then wait for you to echo a phrase and press continue, and bring that phrase back as the output.",
			Steps: []ControlStepRef{
				{Action: "unlock-screen"},
				{Action: "launch-fc"},
				{Action: "echo-prompt-marker", Label: `Echo "<random-chars> Enter a phrase to capture!" through the remote interface`},
				{Action: "robot-take-local", Label: "Find that terminal with the robot and bring it to local control"},
				{Action: "robot-type-leave", Label: `Type echo "" with the robot and leave it on the command line`},
				{Action: "capture-user-echo", Label: "Wait for you to echo a phrase and press continue, then capture it"},
			},
		},
	}
	return actions, sequences
}
