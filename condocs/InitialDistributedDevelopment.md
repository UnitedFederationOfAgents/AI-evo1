# InitialDistributedDevelopment

<!--
```condoc-yaml
condoc:
  startTime: 1789821651
  controlScheme: same-repo
  branch: condoc/InitialDistributedDevelopment-1789821651/main
  callerPath: ..
```
-->

Add a foundation for distributed control for tight SDLC across multiple nodes.


### Step 1 - Create plan documentation and implement 'dev mode' state concept.

[Step 1 Prompt](initialDistributedDevelopmentImpls/Step1Prompt.md)

```prompt
We will add the ability to toggle 'dev mode' to all persistent sub-applications and document the features we'll add to our initial baseline.

The sub-applications that we will implement this function for include:
- federation-command
- local-representative
- agent-coordinator
- condoccer
- dungeon-keeper

The web-UI driven projects will present easily recognizable but visually subtle green borders around the major panel borders when launched in dev mode. (We like the greens chosen so far for things like 'healthy' and 'connected' and their palette)

The dev mode federation-command instances have the square brackets in the prompt coloured green (the ones enclosing the blinker).

To launch any of the applications in dev mode an argument may be supplied - at this point we do not want to introduce dynamic switching at runtime.

Whenever local-representative is launched in dev mode it will launch manages instances in dev mode as well. Dev mode and operations mode instances will refuse to other than the minimum amount necessary to exchange health information and to disclose the mismatch.

In the document we create we will record the following features which we will later implement:
- Loader: A go application which is responsible for launching a sub-application so that it may gain restart and version-switching capabilities
- LR updater: The ability for LR to instruct updates to connected sub-applications
- AC updater: The next hop in the chain, allowing update signals to be broadcast to a set of connected LRs
- Dev branch follower: A mechanism to follow a git branch of in-progress development and propose updates upon change
- The global view in agent-coordinator -- an alternative set of panels to the per-host views we have today which present a net-centric style of interaction
- The global system view in agent-coordinator -- the global mode panel which allows viewing of the working group of participants with health and version information. The location where we can push updates from.
```

### Step 2 - Implement the ufa-loader, starting with local-representative.

[Step 2 Prompt](initialDistributedDevelopmentImpls/Step2Prompt.md)

```prompt
Next we are implementing the ufa-loader (see 'docs/DevMode.md' for some extra guidance).

The loader will work by being a wrapping executable which launches the inner sub-application. The syntax of launching it will be: 'ufa-loader [optional-loader-flags] <binary> [binary-args]' -- for example 'ufa-loader local-representative --dev-mode'

The sub-application will gain a behaviour where it prints a structured section after an identifying banner as the final act before termination. The loader will recognize this and will trigger a restart. We expect that the binary it calls will have been replaced with a more recent version.

We will implement this ufa-loader sub-application to target only local-representative at first, but it will later be expanded to other sub-applications and so we may think ahead and use a common code approach.

We will introduce a 'make run-loader' command to the makefile of all supporting sub-applications.
```


## Human-Prompt

The flow of the condoc is now within the second step.
