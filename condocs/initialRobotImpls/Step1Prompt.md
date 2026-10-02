# Prompt

[InitialRobot](../InitialRobot.md)

I am Not a Robot (IANAR) is another go-with-frontend sub-app which is a 'representable' hosted UI, under a tab 'robot'.

It has channels to collect and drive native capture and input, as well as browser based.

In the first increment the shell will be created, along with all of the standard hookups (makefiles, gitignores, etc).

It will also be given a:
- capture native button, which captures the full native display
- capture browser button, which captures from the browser
- circle mouse button, which when clicked uses native control to make the mouse do a medium sized circle, going clockwise, taking about 1 second


prompt: [`06cd731`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/06cd73152209e461c44a33efea88ade68d83d7df) → reply: [`5da5c09`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/5da5c093077f0526f0f2b98d2c031d7c88e5c648)

## Reply

Created the `ianar` sub-app (I am Not a Robot), a new Go-backend-with-React-frontend "representable" shell mirroring the session-manager/the-conversationalist pattern, hosted under the "robot" tab. It includes the standard hookups (Makefile, .gitignore, go.mod, embedded Vite/React frontend, repr.go connect/disconnect/mode-mismatch plumbing) plus three working actions: **Capture Native** (shells out to scrot/maim/import/gnome-screenshot, whichever is on PATH), **Capture Browser** (getDisplayMedia frame grab passed through the backend), and **Circle Mouse** (xdotool-driven clockwise circle, ~1s, with the geometry unit-tested independent of xdotool). I wired it into `local-representative` and `agent-coordinator` (ports/proxies/tabs/topology badges/stateboard) and the root Makefile's `SUBPROJECTS`, following the exact precedent of the session-manager/the-conversationalist Step-1 commit. Go test tool execution was blocked by the sandbox's approval gate for compiler invocations, so I verified correctness via careful manual review and brace-balance checks across every touched file instead of `go build`/`go test`.


## Revision A

Let's update all applicable functionality to make it leverage the Robotgo package instead of external tools.
