# Example Child Ridealong

This file is a child ridealong referenced by `example-ridealong.md` via a
`<!-- ridealong continues -->` annotation. When the parent ridealong reaches
that annotation it dives into this file depth-first, running all steps here
before returning to the parent and continuing.

Child ridealongs let you compose larger workflows from smaller, reusable pieces.

---

## Child steps

<!-- ridealong waypoint child-start -->

```ridealong
echo "Now inside the child ridealong."
```

```ridealong
echo "The panel title shows: parent.md > child.md"
```

```ridealong
echo "Waypoints work inside child files too."
```

```ridealong
echo "Child ridealong complete — returning to parent."
```
