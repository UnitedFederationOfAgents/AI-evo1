# UFA Command Tree — Next Step

This document drafts the remaining `ufa` subcommand tree, covering only commands
that already exist as FC built-ins. Each entry maps to the existing command so
the implementation can delegate without duplicating logic.

## Proposed Tree

```
ufa
├── help                        (= current default)
├── session
│   ├── help
│   ├── archive                 (implemented in Step 1)
│   ├── list                    (= list-sessions)
│   ├── set <id>                (= set-session <id>)
│   └── clear                   (= clear-session)
├── agent
│   ├── help
│   ├── list                    (= list-agents)
│   └── set <name>              (= set-agent <name>)
└── model
    ├── help
    ├── list                    (= list-models)
    ├── set <name>              (= set-model <name>)
    └── clear                   (= clear-model)
```

## Notes

- All subcommands inherit the same in-process/bubbletea dispatch pattern.
- `ufa session list/set/clear` need access to `m.recordsPath`/`m.sessionDir`
  and must update session state the same way the existing commands do — delegate
  to the same logic blocks rather than duplicating them.
- `ufa agent set` and `ufa model set/clear` need to update prompt style; same as
  the existing `set-agent`/`set-model`/`clear-model` handlers.
- The old command names (`list-sessions`, `set-agent`, etc.) should remain
  functional as aliases so existing ridealong scripts don't break.
