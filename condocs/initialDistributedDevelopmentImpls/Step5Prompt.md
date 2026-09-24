# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Now that we have the requisite sub-components in place we create the mechanism tying everything together.

For a first step we will introduce the condoc lock file.

This file is named '.condoc' and may exist in the root of the repo. The condoccer will create this file whenever it begins working on a new condoc - whenever a state transition other than the completion of the agent's work occurs the file's contents will be updated to:
"Condoccer <performed action> at <human-readable-datetime> (<unix-timestamp>)"

When a condoc enters an "awaiting action" phase specifically after an agent completes work this file will be removed. (Before being re-added again at the next state transition)

The point of this file is to prevent excessive rebuilds.

When this file is present the 'rebuild available' signal will always be false for LR.

When a condoc is complete the file will be removed.
