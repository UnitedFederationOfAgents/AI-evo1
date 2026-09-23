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


### Step 3 - Implement the dev repo watcher mechanism

[Step 3 Prompt](initialDistributedDevelopmentImpls/Step3Prompt.md)

```prompt
Now that we have restart mechanisms available we need the ability to detect changes and automatically rebuild.

We will start by giving this ability to LR, specifically in dev-mode. We can now launch LR with the argument --dev-repo: this automatically sets dev-mode and also marks the current working directory repo as watched. (We must be in a repo for this to be accepted)

When the repo is watched LR will watch the HEAD for changes. If there are changes detected there is an indication in the system tab (a 'rebuild' button becomes available). If the repo is dirty (modified unstaged changes or staged changes) the rebuild button will become orange and say 'dirty'. If the repo is clean there is a periodic check for remote changes and a 'pull --rebase' if there are.

When the rebuild button is active pressing it will cause LR to run 'make deploy-dev-binaries' at the repo root (and indicate while this is progressing).

LR also has an 'auto-rebuild' toggle available. This causes the rebuild to happen automatically when it is detected as possible. The check-and-rebuild process is single-threaded to avoid conflicts.
```


### Step 4 - Implement the global view for agent-coordinator

[Step 4 Prompt](initialDistributedDevelopmentImpls/Step4Prompt.md)

```prompt
Next we will implement the 'global' view in agent-coordinator.

Currently agent-coordinator always has a view contextualized on a single host, now we will add a 'global' selection that sits about the list of hosts. This global selection is mutually exclusive with a particular host. When agent-coordinator first displays it defaults to the global view.

When in the global view every tab has an alternative 'global' view (if implemented). During this step we will implement the beginnings of the 'system' tab and mark all others as 'not yet implemented'.

In the implementation of the 'system' tab we will have a set of nested tabs for the selection of the main view. We will have 'topology' and 'timeline' tabs within the system tab, and we will populate topology this increment - we will not make it interactive yet, we will just make it visible.

For guidance on what the topology tab view within the global system tab view looks like we will reference condocs/initialDistributedDevelopmentImpls/global_topology_panel.jpg -- this image is rotated 90 degrees. We have a main pane and a details-and-control right-side pane.

For now we will populate dummy images in the main pane (these will later represent our hosts) and we will populate dummy readouts in the details-and-control pane.
```


## Human-Prompt

The flow of the condoc is now within the fourth step.
