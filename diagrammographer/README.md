# diagrammographer

diagrammographer (**dg**) is a hosted-UI sub-app for making static and
dynamic diagrams. It's a Go server with an embedded React frontend, like
composer. local-representative launches it as the **diagram** tab, and
agent-coordinator shows it per host.

## Using it

The left-hand bar lists the diagrams. So far these are the examples in
`examples/`, built into the binary, two dynamic and two static:

| | |
| --- | --- |
| `condoc-interaction` | dynamic: one condoc interaction, prompt to pushed commit |
| `dg-plugin-path` | static: how a browser reaches the diagram tab |
| `diagram-anatomy` | static: what a diagram's DTT document holds |
| `diagram-exports` | dynamic: how the video export works, step by step |

The right-hand pane has two views:

- **canvas** shows the diagram the same way the web export does. You can
  drag the nodes around. A bar above the diagram says whether it's
  **dynamic** or **static**. A dynamic diagram also gets step controls
  (◀ ▶ play): the overview first, then each step, which highlights some
  nodes and edges and dims the rest. **play** loops through the steps,
  1.5 s each, and a dot travels along each highlighted edge. The video
  export plays the same motion; the other exports draw each step still.
- **DTT** shows the diagram's DTT document. Edit it and press **apply** to
  redraw the canvas. Dragging a node on the canvas rewrites the document.

Edits stay in the browser. Nothing is saved back to the server, and
**reset** reloads the diagram as it was.

The **export** buttons:

| | |
| --- | --- |
| PNG | the step on the canvas, at 2x |
| GIF | every step (one frame if the diagram is static); the server assembles it |
| video | every step as the canvas plays it, at 30 fps with the moving dots, recorded to WebM in the browser; takes as long as it plays |
| web | a standalone HTML page with the SVGs and the same step controls as the canvas |
| D2 | [D2](https://d2lang.com) diagram-as-code; each step becomes a D2 scenario |
| DTT | the DTT document |

Every image export is drawn from the same SVG as the canvas
(`frontend/src/Diagram.tsx`). D2 does its own layout, so node positions
and the canvas size don't carry over to it.

## The DTT format

A diagram is a DTT document ([dtt/README.md](../dtt/README.md)) with one
statement, `diagrammographer.diagrams`. It selects the diagram by name and
asserts what it contains:

```yaml
# Comments at the top are kept with the diagram.

diagrammographer.diagrams:
  select:
    name: hello                  # lower-case, digits, dashes
  assert:
    title: Hello
    description: Optional, shown under the canvas.
    size: [480, 200]             # optional: the nodes' extent plus 40
    nodes:
      a:                         # node id
        label: A                 # default: the id
        shape: rect              # rect (default), round, circle, cylinder
        color: "#2d4a6b"         # optional fill
        at: [40, 70]             # top-left corner; required
        size: [140, 56]          # the default
      b:
        at: [300, 70]
    edges:
      - from: a
        to: b
        label: says hello        # optional
    steps:                       # optional; a diagram with steps is dynamic
      - caption: A says hello.
        highlight: [a, a->b]     # node ids and from->to edges
```

dtt has no `diagrammographer` tool yet, so dtt can't run these documents.
For now they are only the diagram's source. diagrammographer reads them
with a copy of the repo's YAML-subset reader (`yamlite.go`).

## Wiring

| | |
| --- | --- |
| Port | `--port` 8091; local-representative's `--dg-port` |
| Name to local-representative | `dg` (`--name`), also its plug-in app id |
| Tab | **diagram** on both hosts (app id `dg`) |
| Served at | `/apps/dg/` on local-representative, `/host/<id>/apps/dg/` on agent-coordinator |
| Manifest | version 2, `version` `"2"`, icon mask (`plugin.go`) |

diagrammographer was a plug-in from the start ([docs/PluginSubApps.md](../docs/PluginSubApps.md)),
so, unlike the earlier sub-apps, it has no legacy `/dg/` proxy. Both hosts
frame the tab only once dg is online in that LR's plug-in registry. Until
then they show a launch panel, or "waiting for its hello".

## HTTP API

| | |
| --- | --- |
| `GET /api/status` | `{app_id, version, dev_mode, repr_status}` |
| `GET /api/diagrams` | the list: `[{name, title, steps}]` |
| `GET /api/diagrams/<name>` | `{diagram, dtt, d2}` for one example |
| `POST /api/convert` | `{dtt}` or `{diagram}` → `{diagram, dtt, d2}`, or 400 `{error}` |
| `POST /api/export/gif` | `{frames: [PNG data URLs], delay_ms}` → `image/gif` |

## Building and testing

```
make build        # frontend, then the Go binary
make test         # go test ./...
make dev          # Go server without the embedded frontend
make dev-frontend # Vite dev server, proxying /ws and /api to :8091
```

As with composer, `go test` needs `frontend/dist` to exist, because it's
embedded. Run `make build` once, or `make clean`, which writes a placeholder.
