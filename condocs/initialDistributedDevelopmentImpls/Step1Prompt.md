# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

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
