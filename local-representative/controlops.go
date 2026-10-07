package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file is the ops a control action's instructions can use (see
// controllib.go): the ones LR carries out itself, checking each effect from
// what the sub-apps report back -- an FC instance's control state and
// output -- rather than trusting that keys were sent; and robot.<op>, any of
// IANAR's sequence-v2 ops (ianar/seqv2_ops.go), which LR hands to the robot
// ("__robot:run", see control.go's robotRun). IANAR reports its ops when it
// connects ("robot-ops"), so the definer can offer them with their
// arguments.

// robotOpPrefix marks an instruction as one of IANAR's ops.
const robotOpPrefix = "robot."

// ControlOpArg documents one argument of an op.
type ControlOpArg struct {
	Name     string `json:"name"`
	Help     string `json:"help"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
}

// ControlOpSpec is an op. Describe is how a step lists the instruction,
// with {arg} replaced by the argument's value. The JSON shape matches
// IANAR's opSpec, which is how its ops arrive.
type ControlOpSpec struct {
	Op       string         `json:"op"`
	Summary  string         `json:"summary"`
	Describe string         `json:"describe"`
	Args     []ControlOpArg `json:"args"`
	run      func(r *controlRun, a opArgs) (string, error)
}

func (s ControlOpSpec) arg(name string) *ControlOpArg {
	for i := range s.Args {
		if s.Args[i].Name == name {
			return &s.Args[i]
		}
	}
	return nil
}

func (s ControlOpSpec) argNames() string {
	if len(s.Args) == 0 {
		return "nothing"
	}
	names := make([]string, len(s.Args))
	for i, a := range s.Args {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// opArgs are an instruction's arguments, filled in and with defaults
// applied.
type opArgs map[string]string

func (a opArgs) duration(name string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(a[name]))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s %q isn't a duration like 10s or 1m", name, a[name])
	}
	return d, nil
}

func (a opArgs) flag(name string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(a[name])) {
	case "", "false", "no", "0", "off":
		return false, nil
	case "true", "yes", "1", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s should be true or false, not %q", name, a[name])
}

// findControlOp finds op among LR's own ops and, for robot.<op>, robotOps
// (already prefixed).
func findControlOp(op string, robotOps []ControlOpSpec) (ControlOpSpec, bool) {
	list := controlOps
	if strings.HasPrefix(op, robotOpPrefix) {
		list = robotOps
	}
	for _, s := range list {
		if s.Op == op {
			return s, true
		}
	}
	return ControlOpSpec{}, false
}

var describeArgRe = regexp.MustCompile(`\{[a-z_]+\}`)

// describeControlInstruction renders an instruction as a step lists it.
func describeControlInstruction(in map[string]string, robotOps []ControlOpSpec) string {
	spec, ok := findControlOp(in["op"], robotOps)
	if !ok || spec.Describe == "" {
		// An op we don't know the shape of (IANAR hasn't said): op and args.
		var parts []string
		for k, v := range in {
			if k != "op" {
				parts = append(parts, k+"="+strconv.Quote(v))
			}
		}
		sort.Strings(parts)
		return strings.TrimSpace(in["op"] + " " + strings.Join(parts, " "))
	}
	return describeArgRe.ReplaceAllStringFunc(spec.Describe, func(m string) string {
		name := m[1 : len(m)-1]
		v := in[name]
		if v == "" {
			if a := spec.arg(name); a != nil {
				v = a.Default
			}
		}
		return v
	})
}

// robotControlOps turns IANAR's ops (as it reports them) into robot.<op>
// specs. They have no run: robot instructions are batched into robot runs
// (see controlActionRunner).
func robotControlOps(ops []ControlOpSpec) []ControlOpSpec {
	out := make([]ControlOpSpec, 0, len(ops))
	for _, o := range ops {
		o.Op = robotOpPrefix + o.Op
		o.Summary = "robot: " + o.Summary
		o.Describe = "robot: " + o.Describe
		o.run = nil
		out = append(out, o)
	}
	return out
}

// ---- LR's own ops ----

var (
	// fcArg defaults to the instance launch-fc saves by default.
	fcArg   = ControlOpArg{Name: "fc", Help: "the federation-command instance, as a launch-fc saved it", Default: "{{fc}}"}
	hintArg = ControlOpArg{Name: "hint", Help: "added to the error if this fails, e.g. what probably went wrong"}
)

// controlOps is LR's own ops. It's set from lrControlOps in init rather
// than initialized directly: the ops' run funcs reach back to controlOps
// (through the control state's broadcasts, which snapshot the library),
// and Go rejects that as an initialization cycle.
var controlOps []ControlOpSpec

func init() { controlOps = lrControlOps }

var lrControlOps = []ControlOpSpec{
	{
		Op:       "launch-fc",
		Summary:  "Launch a new federation-command instance as the system tab does, and wait for it to connect under its own name, in remote control. Only a connection made since the launch counts, so an instance already running can't take its place.",
		Describe: "launch a new federation-command, saved as {{{save_as}}}",
		Args: []ControlOpArg{
			{Name: "save_as", Help: "name to save the new instance under, for later instructions' fc", Default: "fc"},
			{Name: "timeout", Help: "how long to wait for it to connect", Default: controlLaunchTimeout.String()},
		},
		run: opLaunchFC,
	},
	{
		Op:       "random",
		Summary:  "Save a random string of lower-case letters and digits (none OCR mixes up, like 0/o or 1/l).",
		Describe: "save {length} random characters as {{{save_as}}}",
		Args: []ControlOpArg{
			{Name: "save_as", Help: "name to save it under", Required: true},
			{Name: "length", Help: "how many characters", Default: "6"},
		},
		run: opRandom,
	},
	{
		Op:       "set",
		Summary:  "Save a value (which may use {{name}} references) under a name.",
		Describe: "save {value} as {{{save_as}}}",
		Args: []ControlOpArg{
			{Name: "save_as", Help: "name to save it under", Required: true},
			{Name: "value", Help: "the value"},
		},
		run: opSet,
	},
	{
		Op:       "show",
		Summary:  "Show a value with the run, beside its steps.",
		Describe: "show {label}: {value}",
		Args: []ControlOpArg{
			{Name: "label", Help: "what it is", Required: true},
			{Name: "value", Help: "the value"},
		},
		run: opShow,
	},
	{
		Op:       "fc-send",
		Summary:  "Send a command to a federation-command instance over the remote interface, optionally waiting for it to print a line.",
		Describe: "send {command} to {fc}",
		Args: []ControlOpArg{
			fcArg,
			{Name: "command", Help: "the command line to send", Required: true},
			{Name: "expect", Help: "a line (after trimming) to wait for it to print; empty to not wait"},
			{Name: "exclusive", Help: "with expect, fail if another instance printed that line too (the robot couldn't tell their terminals apart)", Default: "true"},
			{Name: "timeout", Help: "how long to wait for expect", Default: controlOutputTimeout.String()},
		},
		run: opFCSend,
	},
	{
		Op:       "fc-expect-output",
		Summary:  "Wait for a federation-command instance to print a line, since this step started -- failing at once, with fail_text, on a line that says it went wrong.",
		Describe: "wait for {fc} to print {text}",
		Args: []ControlOpArg{
			fcArg,
			{Name: "text", Help: "the line (after trimming)", Required: true},
			{Name: "fail_text", Help: "fail as soon as a line (after trimming) starts with this, unless it's text itself -- e.g. \"<marker> exit=\" to wait for \"<marker> exit=0\" and fail on any other exit status"},
			{Name: "since", Help: "step: only lines printed since this step started; run: since the run started -- for a command sent steps earlier, whose output may arrive while a step in between (an ask-user, say) is still waiting", Default: "step"},
			{Name: "timeout", Help: "how long to wait", Default: controlOutputTimeout.String()},
			hintArg,
		},
		run: opFCExpectOutput,
	},
	{
		Op:       "fc-expect-state",
		Summary:  "Wait for a federation-command instance to report a control state.",
		Describe: "wait for {fc} to be in {state}",
		Args: []ControlOpArg{
			fcArg,
			{Name: "state", Help: "remote-control or local-control", Required: true},
			{Name: "timeout", Help: "how long to wait", Default: controlStateTimeout.String()},
			hintArg,
		},
		run: opFCExpectState,
	},
	{
		Op:       "fc-require-window",
		Summary:  "Fail unless a federation-command instance has a window on screen for the robot to find (not a detached tmux/screen session).",
		Describe: "check {fc} has a window on screen",
		Args:     []ControlOpArg{fcArg},
		run:      opFCRequireWindow,
	},
	{
		Op:       "node-capture",
		Summary:  "Have a node's robot take a native capture of its screen and save it as a screenshot in the files tab. Another node's is asked for through agent-coordinator, saved in that node's files tab and copied into this one's.",
		Describe: "screenshot {node} with its robot",
		Args: []ControlOpArg{
			{Name: "node", Help: "the node, as agent-coordinator names it -- usually a node control's {{name}}", Required: true},
			{Name: "name", Help: "part of the screenshot's file name; empty for the node's name"},
			{Name: "timeout", Help: "how long to wait for the node's robot", Default: controlCaptureTimeout.String()},
			{Name: "wait_robot", Help: "how long to keep asking while the node's robot isn't running yet (just after the node started, say); empty to fail at once"},
		},
		run: opNodeCapture,
	},
	{
		Op:       "node-wait-connected",
		Summary:  "Wait for a node to be connected to agent-coordinator -- with fresh, for it to connect anew (after a reboot or rebuild), not just to still be connected -- and save the name it connected under.",
		Describe: "wait up to {timeout} for {node} to connect to agent-coordinator",
		Args: []ControlOpArg{
			{Name: "node", Help: "the node, as agent-coordinator names it -- or, with prefix, its host name", Required: true},
			{Name: "prefix", Help: "true: a node named <node>-<anything> counts too (nodes are named <hostname>-<4 characters>, which a rebuilt node draws anew)", Default: "false"},
			{Name: "fresh", Help: "true: only a connection made since this step started counts", Default: "false"},
			{Name: "save_as", Help: "name to save the connected node's name under, for later instructions' node"},
			{Name: "timeout", Help: "how long to wait", Default: controlNodeWaitTimeout.String()},
			hintArg,
		},
		run: opNodeWaitConnected,
	},
	{
		Op:       "node-fetch-file",
		Summary:  "Copy files from a node into this node's files tab: a path, or a glob matching several. The node only hands over files its local-representative's control-fetch-allow setting allows. Another node's are asked for through agent-coordinator, as node-capture does.",
		Describe: "fetch {path} from {node}",
		Args: []ControlOpArg{
			{Name: "node", Help: "the node, as agent-coordinator names it -- usually a node control's {{name}}", Required: true},
			{Name: "path", Help: "an absolute path or glob (*, ?, [...]); ~ is the node user's home", Required: true},
			{Name: "max_files", Help: "the most files a glob may bring back", Default: "20"},
			{Name: "max_size", Help: "the most bytes kept of each file (a longer one keeps its end, as a log's last lines)", Default: "8388608"},
			{Name: "optional", Help: "true: matching nothing (or nothing readable) isn't an error", Default: "false"},
			{Name: "timeout", Help: "how long to wait for the node's answer", Default: controlFetchTimeout.String()},
			hintArg,
		},
		run: opNodeFetchFile,
	},
	{
		Op:       "ask-user",
		Summary:  "Show a message in the control tab with a continue button, and wait until someone presses it (or the run is cancelled).",
		Describe: "ask: {message} -- then wait for continue",
		Args: []ControlOpArg{
			{Name: "message", Help: "what the person running the sequence should do before pressing continue", Required: true},
			{Name: "timeout", Help: "how long to wait for continue", Default: controlAskTimeout.String()},
		},
		run: opAskUser,
	},
	{
		Op:       "fc-capture-echo",
		Summary:  "Find the last echo command typed into a federation-command instance (in local control) since this step started, and save the phrase it printed.",
		Describe: "save the phrase last echoed in {fc} as {{{save_as}}}",
		Args: []ControlOpArg{
			fcArg,
			{Name: "save_as", Help: "name to save the phrase under", Default: "phrase"},
			{Name: "timeout", Help: "how long to wait for the echo and its output to arrive", Default: controlEchoTimeout.String()},
			hintArg,
		},
		run: opFCCaptureEcho,
	},
	{
		Op:       "output",
		Summary:  "Show a value as the run's output, under its steps, ahead of the other values.",
		Describe: "output {label}: {value}",
		Args: []ControlOpArg{
			{Name: "label", Help: "what it is", Required: true},
			{Name: "value", Help: "the value"},
		},
		run: opOutput,
	},
}

// fcFrom returns the fc argument, which must name an instance.
func fcFrom(a opArgs) (string, error) {
	fc := strings.TrimSpace(a["fc"])
	if fc == "" || strings.Contains(fc, "{{") {
		return "", fmt.Errorf("no federation-command instance given (fc is %q) -- launch one with launch-fc first", a["fc"])
	}
	return fc, nil
}

// opLaunchFC launches a new FC and waits for it to connect. It only accepts
// a connection under the new instance's own name, made since the launch:
// commands are routed by that name, so an instance sharing it -- an older
// federation-command build connecting as the bare "federation-command", or
// one already running when the sequence started -- would take commands
// meant for the new one, leaving the robot nothing to find on the new
// terminal (Step3Prompt.md Revision A).
func opLaunchFC(r *controlRun, a opArgs) (string, error) {
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	saveAs := strings.TrimSpace(a["save_as"])
	if !ctlNameRe.MatchString(saveAs) {
		return "", fmt.Errorf("save_as %q isn't a valid name", saveAs)
	}
	before := r.s.fcKeys()
	id, err := r.s.launchManaged(fcAppName)
	if err != nil {
		return "", fmt.Errorf("launching federation-command: %v", err)
	}
	r.vars[saveAs+"_instance"] = id
	r.e.addValue("federation-command instance", id)
	if others := len(before); others > 0 {
		r.note(fmt.Sprintf("launched %s (%d other instance(s) already connected); waiting for it to connect…", id, others))
	} else {
		r.note(fmt.Sprintf("launched %s; waiting for it to connect…", id))
	}

	var key string
	ok, err := r.waitFor(timeout, func() bool {
		key = r.s.fcKeyForInstanceID(id)
		return key == id && !before[key] && r.s.fcState(key) == "remote-control"
	})
	if err != nil {
		return "", err
	}
	if !ok {
		switch {
		case key == "":
			return "", fmt.Errorf("%s didn't connect to this local-representative within %s", id, timeout)
		case key != id:
			return "", fmt.Errorf("%s connected as %q rather than under its own name, so commands for it can reach another instance -- its federation-command binary predates per-instance names; rebuild it", id, key)
		case before[key]:
			return "", fmt.Errorf("an instance calling itself %s was connected before the launch, so the new one can't be told apart from it", id)
		default:
			return "", fmt.Errorf("%s connected but is in %s, not remote control", id, orNone(r.s.fcState(key)))
		}
	}
	r.vars[saveAs] = key
	note := fmt.Sprintf("%s is connected in remote control", key)
	if hint := r.s.managedDetachedHint(id); hint != "" {
		note += " (" + hint + ")"
	}
	return note, nil
}

func opRandom(r *controlRun, a opArgs) (string, error) {
	n, err := strconv.Atoi(strings.TrimSpace(a["length"]))
	if err != nil || n < 1 || n > 64 {
		return "", fmt.Errorf("length %q should be a number from 1 to 64", a["length"])
	}
	return "", r.save(a["save_as"], randomToken(n))
}

func opSet(r *controlRun, a opArgs) (string, error) {
	return "", r.save(a["save_as"], a["value"])
}

func opShow(r *controlRun, a opArgs) (string, error) {
	r.e.addValue(a["label"], a["value"])
	return "", nil
}

func opFCSend(r *controlRun, a opArgs) (string, error) {
	fc, err := fcFrom(a)
	if err != nil {
		return "", err
	}
	expect := strings.TrimSpace(a["expect"])
	exclusive, err := a.flag("exclusive")
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	mark := r.e.logMark()
	r.s.sendFCCommand(fc, a["command"])
	if expect == "" {
		return fmt.Sprintf("sent to %s", fc), nil
	}
	ok, err := r.waitForOutput(fc, mark, expect, timeout)
	if err != nil {
		return "", err
	}
	other := markerPrintedBy(r.e.logsSince(mark), expect, fc)
	if !ok {
		if other != "" {
			return "", fmt.Errorf("%q came back from %s, not %s -- the command reached the wrong instance", expect, other, fc)
		}
		return "", fmt.Errorf("%s didn't print %q within %s", fc, expect, timeout)
	}
	if exclusive && other != "" {
		return "", fmt.Errorf("%s printed %q, but so did %s -- the robot can't tell their terminals apart", fc, expect, other)
	}
	return fmt.Sprintf("%s printed %q", fc, expect), nil
}

func opFCExpectOutput(r *controlRun, a opArgs) (string, error) {
	fc, err := fcFrom(a)
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	mark := r.stepMark
	switch strings.TrimSpace(a["since"]) {
	case "", "step":
	case "run":
		mark = 0 // the run's FC log starts empty
	default:
		return "", fmt.Errorf("since %q should be step or run", a["since"])
	}
	text := strings.TrimSpace(a["text"])
	failText := strings.TrimSpace(a["fail_text"])
	var failed string
	ok, err := r.waitFor(timeout, func() bool {
		for _, ev := range r.e.logsSince(mark) {
			if ev.fc != fc || ev.kind != "output" {
				continue
			}
			line := strings.TrimSpace(ev.line)
			if line == text {
				return true
			}
			if failText != "" && strings.HasPrefix(line, failText) {
				failed = line
				return true
			}
		}
		return false
	})
	switch {
	case err != nil:
		return "", err
	case failed != "":
		return "", fmt.Errorf("%s printed %q rather than %q", fc, failed, text)
	case !ok:
		return "", fmt.Errorf("%s didn't print %q within %s", fc, text, timeout)
	}
	return fmt.Sprintf("%s printed %q", fc, text), nil
}

func opFCExpectState(r *controlRun, a opArgs) (string, error) {
	fc, err := fcFrom(a)
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	want := strings.TrimSpace(a["state"])
	if want != "remote-control" && want != "local-control" {
		return "", fmt.Errorf("state %q should be remote-control or local-control", want)
	}
	ok, err := r.waitFor(timeout, func() bool { return r.s.fcState(fc) == want })
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s is still in %s, not %s, after %s", fc, orNone(r.s.fcState(fc)), want, timeout)
	}
	return fmt.Sprintf("%s is in %s", fc, want), nil
}

// opAskUser hands the run to the person following it (Step3Prompt.md
// Revision G): the control tab shows the message with a continue button
// (ControlRunMsg.Prompt) until it's pressed.
func opAskUser(r *controlRun, a opArgs) (string, error) {
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	ch := r.e.setPrompt(r.step, strings.TrimSpace(a["message"]))
	defer r.e.clearPrompt()
	r.note("waiting for continue…")
	select {
	case <-ch:
		return "continued", nil
	case <-r.cancel:
		return "", errControlCancelled
	case <-time.After(timeout):
		return "", fmt.Errorf("nobody pressed continue within %s", timeout)
	}
}

// opFCCaptureEcho saves what the last echo typed into fc since the step
// started printed. It waits for output matching the echo's own argument; if
// something else came back (a $VAR, say), the first line of that is taken
// once the timeout passes.
func opFCCaptureEcho(r *controlRun, a opArgs) (string, error) {
	fc, err := fcFrom(a)
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	var echo lastEcho
	ok, err := r.waitFor(timeout, func() bool {
		echo = lastEchoIn(r.e.logsSince(r.stepMark), fc)
		return echo.cmd != "" && (echo.arg == "" || echo.printed())
	})
	if err != nil {
		return "", err
	}
	switch {
	case echo.cmd == "":
		return "", fmt.Errorf("no echo command was entered in %s", fc)
	case echo.arg == "":
		return "", fmt.Errorf("%s ran %s, which echoes nothing", fc, echo.cmd)
	case ok:
		return fmt.Sprintf("%s echoed %q", fc, echo.arg), r.save(a["save_as"], echo.arg)
	case len(echo.output) > 0:
		return fmt.Sprintf("%s ran %s and printed %q", fc, echo.cmd, echo.output[0]), r.save(a["save_as"], echo.output[0])
	}
	return "", fmt.Errorf("%s ran %s but printed nothing back within %s", fc, echo.cmd, timeout)
}

// lastEcho is the last echo command an instance ran, what it should print
// and what it did print.
type lastEcho struct {
	cmd, arg string
	output   []string // trimmed, non-empty
}

func (e lastEcho) printed() bool {
	for _, line := range e.output {
		if line == e.arg {
			return true
		}
	}
	return false
}

var echoCmdRe = regexp.MustCompile(`^\s*echo(\s+(.*))?$`)

// lastEchoIn finds fc's last echo command in logs (typed in local control,
// so logged as "cmd") and the output after it.
func lastEchoIn(logs []fcLogEvent, fc string) lastEcho {
	var e lastEcho
	for _, ev := range logs {
		if ev.fc != fc {
			continue
		}
		switch ev.kind {
		case "cmd":
			if m := echoCmdRe.FindStringSubmatch(ev.line); m != nil {
				e = lastEcho{cmd: strings.TrimSpace(ev.line), arg: echoArgument(m[2])}
			}
		case "output":
			if line := strings.TrimSpace(ev.line); e.cmd != "" && line != "" {
				e.output = append(e.output, line)
			}
		}
	}
	return e
}

// echoArgument is roughly what a shell's echo prints for args: one quoted
// argument unquoted (trimmed, as output lines are compared), otherwise the
// words joined by single spaces with their quotes dropped.
func echoArgument(args string) string {
	args = strings.TrimSpace(args)
	n := len(args)
	if n >= 2 && args[0] == '\'' && args[n-1] == '\'' && !strings.Contains(args[1:n-1], "'") {
		return strings.TrimSpace(args[1 : n-1])
	}
	if n >= 2 && args[0] == '"' && args[n-1] == '"' && !strings.Contains(strings.ReplaceAll(args[1:n-1], `\"`, ""), `"`) {
		return strings.TrimSpace(strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(args[1 : n-1]))
	}
	return strings.Join(strings.Fields(strings.NewReplacer(`"`, "", `'`, "").Replace(args)), " ")
}

func opOutput(r *controlRun, a opArgs) (string, error) {
	r.e.addOutput(a["label"], a["value"])
	return "", nil
}

func opFCRequireWindow(r *controlRun, a opArgs) (string, error) {
	fc, err := fcFrom(a)
	if err != nil {
		return "", err
	}
	if hint := r.s.managedDetachedHint(fc); hint != "" {
		return "", fmt.Errorf("%s has no window for the robot to find: %s", fc, hint)
	}
	return "", nil
}
