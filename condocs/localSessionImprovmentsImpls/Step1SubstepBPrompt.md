# Prompt

[Step1Prompt](Step1Prompt.md)

In this revision we will extend the UI behaviour of condoccer so that when we are within a step or substep we may use the same 'enter' style interaction to go into two more nested views - files changed during the increment and the diff of a particular file.

In both cases we will be able to select the list item first, which will bring its content to focus in the main panel (the same way highlighting a revision in a step does now). Having an item selected will allow a subsequent 'enter' interaction. The diff view will show the full file (regular text) and the change occurrences (red/green) will be the list items in the left hand pane. We can highlight them but not drill into them any farther.

This increment of implementation should make due with the current file modification behaviours currently operating in condoccer processes and should not modify the document schema or state machine to accomplish this new view behaviour.

This increment will keep only single-selection, we won't consider any multi-select at this point.
