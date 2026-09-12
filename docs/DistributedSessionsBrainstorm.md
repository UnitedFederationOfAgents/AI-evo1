# Brainstorm Distributed Sessions

Brainstorm some aspects of session behaviour to support distributed sessions. This includes local session behaviours and distributed general behaviours like file management.

## Notes

- Sessions need to fall out of scope -- (this implementation may not be very soon)
  - Max session length is needed and they can point to the continuation
  - Sessions need 'warmth' like 'active', 'recent', '(none)', 'past'

## Questions

- How do we share files?
- How do we telescope context? How do we visualize?
- How do we implement in phases?
- How do we clean? (redact, remove noise)
- How do we summarize and compact?
- How much identification do we need to add? (app? instance? session name vs ID?)
- What local session behaviours do we need to add? How does discovery work? (local and remote)
- How much awareness does each sub-app need?
- Who does the session processing? (Which parts?)

### How do we share files?

- LR works with AC as the backbone.
  * Which sub-apps need to use this?
  * Do sub-apps genererally use the filesystem directly and not normally need networking? (Lean to yes)

- An app that uses sessions 
  * How should we 'use sessions'?
    - Call claudiable, keep knowledge of session specifics there to the extend possible

* Do we need to add UI functionality/general access to file sharing yet?
  - Probably not, better to not entangle

- For current chain under consideration:
  - We run in federation-command
  - We list-sessions, maybe with an arg to specify inclusion of remote
    - When we include remote that means FC needs to initiate discovery; let's assume this means talking to LR
      * Do we ask it directly to search the agent records dir?
        - No, let's just use a wildcard files approach with known paths
    - LR transfers the files, we'll want this to block so the list-sessions can include results.
    - This will probably make sense for the clauditable binary to handle, FC calls CLBL
  - The list of sessions comes back, with indication of which sessions are local, vs remote

### How do we telescope context? How do we visualize?

- Telescope context
  - Using summary files and abbreviated session logs that link to external '-raw' and '-processed' files
  - Let agents use their natural bring-in-files strategy to bring in what is needed
  - Cap length of session files and have natural continuation
    * Does this mean we need a manifest for a session?
      - Maybe a manifest for a session is a good idea generally

* Is there a good place to ALSO keep a 'head' style view? Or does a summary do enough?

- Visualize
  - This would be a good thing to draw/diagram
  - We need to have the right density of summary files at 

### How do we implement in phases?

- Let's consider phases: minimum distributed vs 'later' (undifferentiated for now)
  - mindist
    - We probably don't need to summarize yet at all
    - We need file transfers to work
      - We need sub-apps to be able to invoke file transfers
  - later
    - everything else

### How do we clean? (redact, remove noise)

- Needs to be a layered execution
  - Immediately - synchronous with clbl call
    - Might never want heuristic here
    - First step of the raw-->processed pipeline
  - Continuous - more often than summarizing
    - 

### How do we summarize and compact?

- A system of max sizes across different dimensions; max number of commands, high number of commands with rolling window, wall-clock time

- Two layers of summary are needed, potentially more depending on amount of low level information
  - A direct layer, closest to the actual events, giving a very specific (but not necessarily encompassing) description of what happened, fairly low level
  - A (sub)section layer, bringing direct layer summaries together to give manageable size interface files
  - A layout layer, summarizing which direct and/or section layer summaries link into what groups of events heuristicly and describing the overall activities at a higher level
  - Direct summary uses only events as context, section/layout uses layers below and events

- Compacting is never needed for working state, maybe we compress during a deeper archiving later

### How much identification do we need to add? (app? instance? session name vs ID?)

- We do want name AND ID because we want names that are like summaries (similar to conversation handles in web UI models)
  - We need to see host -- hosts will need a name and ID (this will help for deconfliction and instance lifetimes)
  - We need to see the user agent and head (instanceID)

* Will ID need to change?
  - This will be a question for later we can think about it at the same time as joining/mutating sessions

### What local session behaviours do we need to add? How does discovery work? (local and remote)

* Will we need session manager?
  - Ideally session manager is not needed for any of the lowest layer functionality/can call clbl for any of this
  - We need session manager to see session depiction outside of the use of other sub-apps
  - We don't need SM for anything like listing sessions, setting the session to use

- We need some foundational identification pieces
  - We need to have whatever behaviour is used for 'join current default session and notify'
  - We need to add the session manifest as the data store for some metadata that doesn't fit in folder name (like name)
  - We need host ID/name, head ID, user agent

- Discovery works on the local filesystem (through clbl call) but may pull remote data first

- We don't NEED cli 'ufa session' entrypoint but it is worth it to make things smoother

- Chain of events
  - federation-command launches
  - FC uses 'get-default-session' to get the current default or detect if there is none
    - Notify which session is used or create one automatically if not present, indicating it is a default
    - For the moment we assume defaults are per-day
  - A default session gets the name "<date> Default"
  - A command is run and it clbls to the chosen default location

- Notes
  - We will need to support concurrency properly
    - Lock file creation when execution begins, when another writer detects lock it uses a 'tributary input'
    - When sessions are remote always use the 'tributary input'
  * When does a non-default get created? On set-session or first call to the session?
    - Let's have it on 'set-session'/'new-session'

* Do sessions need owners?

### How much awareness does each sub-app need of the mechanics of sessions?

- Very little ideally

### Who does the session processing? (Which parts?)

- The synchronous parts are ideal right in clbl
- Maybe session-manager is the right place for everything else so it can add that self-awareness to the picture

----------------------------------------------------------------

## Working Section -- concise extra documentation about decisions taken during implementation
