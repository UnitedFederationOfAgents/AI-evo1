# Prompt

[Step1Prompt](Step1Prompt.md)

We now have the robot compiling, but we are getting a crash when we try to capture native:

8:55:32 AM  [robot]!   Major opcode of failed request:  73 (X_GetImage)
8:55:32 AM  [robot]!   Serial number of failed request:  7
8:55:32 AM  [robot]!   Current serial number in output stream:  7

Let's either resolve this if it is obvious or discuss steps we can take to analyze the failure if it is not.


prompt: [`1a6d556`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/1a6d556a1e0612558afe55fa4eb8ec4499907eed) → reply: [`74fc78c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/74fc78c590eae536e8e68484465c3aec8c95bde8)

## Reply

(no reply captured)


## Revision A

We now have the robot compiling, but we are getting a crash when we try to capture native:

8:55:32 AM  [robot]!   Major opcode of failed request:  73 (X_GetImage)
8:55:32 AM  [robot]!   Serial number of failed request:  7
8:55:32 AM  [robot]!   Current serial number in output stream:  7

Let's either resolve this if it is obvious or discuss steps we can take to analyze the failure if it is not.


(Let's try this again...)


prompt: [`3827361`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3827361a85da6e529eec0742e58ef0f44395a65d) → reply: [`785db15`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/785db152aef9e718cab78c90ed495f0cfaf7bf80)

## Reply A

I tracked the X_GetImage crash to a goroutine-safety bug rather than an environment/build issue: `main.go`'s WebSocket loop dispatches *every* incoming message, including `capture-native` and `circle-mouse`, onto its own fresh goroutine (`go s.handleClientMsg(c, m)`), with nothing serializing access to robotgo. On Linux, robotgo's capture/input calls are cgo wrappers around Xlib, which isn't safe for concurrent access from multiple goroutines/OS threads without `XInitThreads()` (which robotgo never calls) — so an overlapping capture-native click, or a capture firing mid circle-mouse drive, corrupts the X11 request stream, producing exactly the serial-number-mismatched `X_GetImage` error reported and crashing the process via Xlib's default error handler.

I fixed it in `ianar/robot.go` by adding a package-level `robotMu sync.Mutex` and holding it around every robotgo entry point: the capture call in `captureNativeDisplay()`, and the whole `circleMouse()` drive (location read through the full ~1s stepped move), each with a comment explaining why. I couldn't run `go build`/`go vet`/`go test` myself — same compiler-invocation approval gate noted in every prior reply on this step — so this still needs a real build/test pass to confirm.


## <REPLACE-Revision|Retry> B

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
