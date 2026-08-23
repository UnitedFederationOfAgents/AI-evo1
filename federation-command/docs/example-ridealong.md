# Example Ridealong

A ridealong is a guided, step-by-step execution sequence parsed from a markdown file.
Each ` ```ridealong ` block contains commands that run one at a time — you confirm each
step before it runs, review the previous result, and navigate freely with waypoints.

This example uses only `echo` commands so it has no side-effects and is safe to run
anywhere.

## How to start this ridealong

From inside federation-command (or from the shell):

```
ridealong federation-command/docs/example-ridealong.md
```

Or jump to a specific section:

```
ridealong --waypoint greet federation-command/docs/example-ridealong.md
```

---

## Introduction

<!-- ridealong waypoint start -->

```ridealong
echo "Welcome to the example ridealong!"
```

```ridealong
echo "Each step shown in the panel is a single command."
```

```ridealong
echo "Press execute (or Enter) to run the highlighted step."
```

---

## Greetings

<!-- ridealong waypoint greet -->

```ridealong
echo "Hello, world!"
```

```ridealong
echo "Ridealongs can have as many steps as you like."
```

```ridealong
agent -a clod -p "We can check if our agent is up and running - are you up and running agent?"
```

```ridealong
echo "The previous step's exit code appears if it was non-zero."
```

---

## Child ridealong demo

The next step dives into a child ridealong file. Control returns here when it finishes.

[example-child-ridealong.md](./example-child-ridealong.md) <!-- ridealong continues -->

---

## Wrap-up

<!-- ridealong waypoint done -->

```ridealong
echo "Back in the parent ridealong after the child completed."
```

```ridealong
echo "Ridealong finished — press exit or let it complete naturally."
```
