# Prompt

[InitialRobot](../InitialRobot.md)

Now that we have control sequences working nicely we will make the recording capabilities more flexible.

In the composer, to the right of the main 'steps' blocks we will allow much slimmer 'recording' blocks to be created. These blocks may be longer than a single step (one block may span N steps). They may overlap one-another, and there may be up to 3 in parallel.

Each one of these blocks represents a recording being taken on one node involved in the sequence. The same node may not be in two blocks which are side-by-side for any particular step.

These recordings are gathered in the sequence collection.

This will allow us to do things like only record important segments, and to record sequences which are in differing locations.

Let's begin this implementation now.
