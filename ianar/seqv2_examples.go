package main

// This file is the sequence-v2 library IANAR starts with (and restores on
// "Restore examples"): general-purpose definer actions, and three composer
// sequences built from them (condocs/initialRobotImpls/Step2Prompt.md,
// Revision G):
//
//   - fc-hello-world: sequence-v1's federation-command "hello world",
//     rebuilt from generic actions.
//   - firefox-weather: opens a private Firefox window from Firefox's icon
//     in the dock, goes to a weather page for a country (a control,
//     Portugal by default) and prints the temperature it shows. The page is
//     wttr.in's one-line plain-text format, which reads reliably by OCR.
//   - desktop-text-file: creates a text document on the desktop, opens it,
//     writes "hello world" in it, saves a screenshot to the desktop and
//     deletes the document.

// tempPattern matches a temperature as OCR reads one: "+22°C", "-3 °F",
// "21.5°C", with the degree sign possibly misread or dropped.
const tempPattern = `([+-]?\d+(?:[.,]\d+)?\s*[°º˚*o]?\s*[CF])\b`

func exampleLibrary() ([]ActionDef, []SequenceV2) {
	actions := []ActionDef{
		{
			ID: "wake-screen", Name: "Wake the screen",
			Description: "Check the screen is unlocked, and wake it if it has blanked. Fails on a locked screen.",
			Do:          []Instruction{{"op": "ensure-awake"}},
		},
		{
			ID: "select-window", Name: "Select a window by its title",
			Description: "Wake the screen, then find the window's title bar on screen and click it.",
			Controls:    []Control{{Name: "title", Label: "Window title", Default: "federation-command", Help: "the title exactly as the title bar shows it"}},
			Do: []Instruction{
				{"op": "ensure-awake"},
				{"op": "focus-window", "title": "{{title}}"},
			},
		},
		{
			ID: "press-keys", Name: "Press keys",
			Description: "Press keys in the window that has focus.",
			Controls:    []Control{{Name: "keys", Label: "Keys", Default: "enter", Help: "chords separated by spaces: end, ctrl+u, ctrl+shift+p, alt+f2"}},
			Do:          []Instruction{{"op": "key", "keys": "{{keys}}"}},
		},
		{
			ID: "type-text", Name: "Type text",
			Description: "Type text into the window that has focus.",
			Controls:    []Control{{Name: "text", Label: "Text", Default: "hello world"}},
			Do:          []Instruction{{"op": "type", "text": "{{text}}"}},
		},
		{
			ID: "wait", Name: "Wait",
			Controls: []Control{{Name: "duration", Label: "How long", Default: "1s"}},
			Do:       []Instruction{{"op": "wait", "duration": "{{duration}}"}},
		},
		{
			ID: "submit-and-check", Name: "Submit a command and check its output appears",
			Description: "Count the lines showing the output, press the keys, and check one more line shows it afterwards -- so keys that went somewhere else fail the step.",
			Controls: []Control{
				{Name: "output", Label: "Expected output", Default: "hello world!"},
				{Name: "command", Label: "The command", Default: `echo "hello world!"`, Help: "lines showing the command itself don't count as output"},
				{Name: "keys", Label: "Keys to submit", Default: "enter"},
				{Name: "timeout", Label: "Time for it to appear", Default: "3s"},
			},
			Do: []Instruction{
				{"op": "count-text", "text": "{{output}}", "exclude": "{{command}}", "save_as": "output_before"},
				{"op": "key", "keys": "{{keys}}"},
				{"op": "wait", "duration": "1s"},
				{"op": "wait-for-text", "text": "{{output}}", "exclude": "{{command}}", "more_than": "{{output_before}}", "timeout": "{{timeout}}"},
			},
		},
		{
			ID: "open-private-firefox", Name: "Open a Firefox private window from its icon",
			Description: "Right-click Firefox's icon (in the dock) and choose New Private Window from its menu.",
			Controls: []Control{
				{Name: "app", Label: "App", Default: "firefox"},
				{Name: "menu_item", Label: "Menu item", Default: "New Private Window"},
				{Name: "ready_text", Label: "Text showing it's open", Default: "Private"},
			},
			Do: []Instruction{
				{"op": "ensure-awake"},
				// A menu left open (e.g. by an earlier failed run) would
				// swallow the right-click, so close it first.
				{"op": "key", "keys": "escape"},
				{"op": "click-icon", "app": "{{app}}", "button": "right"},
				{"op": "click-text", "text": "{{menu_item}}"},
				{"op": "wait", "duration": "1500ms"},
				{"op": "wait-for-text", "text": "{{ready_text}}", "timeout": "15s"},
			},
		},
		{
			ID: "open-url", Name: "Go to a web address",
			Description: "In the browser window that has focus: select the address bar, type the address and press Enter.",
			Controls:    []Control{{Name: "url", Label: "Address", Default: "https://wttr.in/"}},
			Do: []Instruction{
				{"op": "key", "keys": "ctrl+l"},
				{"op": "type", "text": "{{url}}"},
				{"op": "key", "keys": "enter"},
			},
		},
		{
			ID: "read-temperature", Name: "Read a temperature off the screen",
			Description: "Find a temperature (like +22°C) on the line naming the place, save it as {{temperature}} and print it.",
			Controls: []Control{
				{Name: "place", Label: "Place", Default: "Portugal", Help: "text on the same line as the temperature"},
				{Name: "timeout", Label: "Time for it to appear", Default: "20s"},
			},
			Do: []Instruction{
				{"op": "read-text", "pattern": tempPattern, "near": "{{place}}", "save_as": "temperature", "report": "Temperature in {{place}}", "timeout": "{{timeout}}"},
			},
		},
		{
			ID: "create-file", Name: "Create a file",
			Controls: []Control{
				{Name: "path", Label: "Path", Default: "{{desktop}}/new-file.txt"},
				{Name: "content", Label: "Content"},
				{Name: "overwrite", Label: "Replace an existing file", Default: "false"},
			},
			Do: []Instruction{{"op": "create-file", "path": "{{path}}", "content": "{{content}}", "overwrite": "{{overwrite}}"}},
		},
		{
			ID: "open-file", Name: "Open a file in its default app",
			Description: "Close any open menu, open the file through GNOME's Run dialog (Alt+F2 → xdg-open) so the app opens in front, and wait for one more line showing ready_text than before.",
			Controls: []Control{
				{Name: "path", Label: "Path"},
				{Name: "ready_text", Label: "Text showing it's open", Help: "e.g. the file's name, as the app's title bar shows it"},
			},
			Do: []Instruction{
				// A menu left open holds the keyboard: Alt+F2 and the typed
				// command went into one in Revision H's debug run.
				{"op": "key", "keys": "escape"},
				{"op": "wait", "duration": "1s"},
				{"op": "count-text", "text": "{{ready_text}}", "exclude": "xdg-open", "save_as": "open_before"},
				{"op": "key", "keys": "alt+f2"},
				{"op": "wait", "duration": "1s"},
				{"op": "type", "text": `xdg-open "{{path}}"`},
				{"op": "key", "keys": "enter"},
				// The Run dialog's own "xdg-open ..." line, still fading out,
				// isn't the app opening.
				{"op": "wait-for-text", "text": "{{ready_text}}", "exclude": "xdg-open", "more_than": "{{open_before}}", "timeout": "15s"},
			},
		},
		{
			ID: "save-document", Name: "Save the document",
			Description: "Press the app's save keys and give it a moment to write the file.",
			Controls:    []Control{{Name: "keys", Label: "Keys", Default: "ctrl+s"}},
			Do: []Instruction{
				{"op": "key", "keys": "{{keys}}"},
				{"op": "wait", "duration": "1s"},
			},
		},
		{
			ID: "expect-file", Name: "Check a file's contents",
			Controls: []Control{
				{Name: "path", Label: "Path"},
				{Name: "contains", Label: "Must contain"},
			},
			Do: []Instruction{{"op": "expect-file", "path": "{{path}}", "contains": "{{contains}}"}},
		},
		{
			ID: "save-screenshot", Name: "Save a screenshot",
			Controls: []Control{{Name: "path", Label: "Path", Default: "{{desktop}}/screenshot-{{timestamp}}.png"}},
			Do:       []Instruction{{"op": "save-screenshot", "path": "{{path}}"}},
		},
		{
			ID: "delete-file", Name: "Delete a file",
			Controls: []Control{{Name: "path", Label: "Path"}},
			Do:       []Instruction{{"op": "delete-file", "path": "{{path}}"}},
		},
	}

	sequences := []SequenceV2{
		{
			ID: "fc-hello-world", Name: "federation-command: hello world",
			Description: "sequence-v1's sequence, built from definer actions.",
			Steps: []StepRef{
				{Action: "select-window", Label: "Select the terminal with federation-command", With: map[string]string{"title": "federation-command"}},
				{Action: "press-keys", Label: "Bring federation-command to local control", With: map[string]string{"keys": "right"}},
				{Action: "press-keys", Label: "Bring the cursor to the command line input", With: map[string]string{"keys": "end ctrl+u"}},
				{Action: "type-text", Label: `Enter: 'echo "hello world!"'`, With: map[string]string{"text": `echo "hello world!"`}},
				{Action: "submit-and-check", Label: "Press enter to submit the command", With: map[string]string{"output": "hello world!", "command": `echo "hello world!"`}},
				{Action: "press-keys", Label: "Bring federation-command back to remote control", With: map[string]string{"keys": "left"}},
			},
		},
		{
			ID: "firefox-weather", Name: "Firefox: temperature in a country",
			Description: "Open a private Firefox window from its icon, go to a weather page and print the temperature in the chosen country.",
			Controls:    []Control{{Name: "country", Label: "Country", Default: "Portugal"}},
			Steps: []StepRef{
				{Action: "open-private-firefox", Label: "Open a new private Firefox window from its icon"},
				{Action: "open-url", Label: "Go to the weather page for {{country}}", With: map[string]string{"url": "https://wttr.in/{{country|url}}?format=%l:+%t&m"}},
				{Action: "press-keys", Label: "Zoom in so the page reads clearly", With: map[string]string{"keys": "ctrl+= ctrl+= ctrl+="}},
				{Action: "read-temperature", Label: "Print the temperature in {{country}}", With: map[string]string{"place": "{{country}}"}},
			},
		},
		{
			ID: "desktop-text-file", Name: "Desktop text file: hello world",
			Description: "Create a text document on the desktop, open it, write in it, save a screenshot to the desktop, and delete the document.",
			Controls: []Control{
				{Name: "file", Label: "Document", Default: "{{desktop}}/ianar-hello-world.txt"},
				{Name: "text", Label: "Text to write", Default: "hello world"},
			},
			Steps: []StepRef{
				{Action: "wake-screen"},
				{Action: "create-file", Label: "Create a new text document on the desktop", With: map[string]string{"path": "{{file}}", "overwrite": "true"}},
				{Action: "open-file", Label: "Open the text document", With: map[string]string{"path": "{{file}}", "ready_text": "{{file|base}}"}},
				{Action: "type-text", Label: "Write \"{{text}}\" in it", With: map[string]string{"text": "{{text}}"}},
				{Action: "save-document"},
				{Action: "expect-file", Label: "Check the document holds \"{{text}}\"", With: map[string]string{"path": "{{file}}", "contains": "{{text}}"}},
				{Action: "save-screenshot", Label: "Save a screenshot to the desktop", With: map[string]string{"path": "{{desktop}}/ianar-hello-world-{{timestamp}}.png"}},
				{Action: "press-keys", Label: "Close the document", With: map[string]string{"keys": "ctrl+w"}},
				{Action: "delete-file", Label: "Delete the text document", With: map[string]string{"path": "{{file}}"}},
			},
		},
	}
	return actions, sequences
}
