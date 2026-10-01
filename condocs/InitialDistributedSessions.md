# InitialDistributedSessions

<!--
```condoc-yaml
condoc:
  startTime: 1790777972
  controlScheme: same-repo
  branch: condoc/InitialDistributedSessions-1790777972/main
  callerPath: ..
```
-->

Add the fundamentals of distributed sessions.


### Step 1 - Start by adding a descriptive document and adjusting behaviour slightly.

[Step 1 Prompt](initialDistributedSessionsImpls/Step1Prompt.md)

```prompt
Let's first adjust our logic so that whenever a '-raw' or '-s-raw' file is produced the producer of this file always handles the 'raw-->processed'.

Let's also create an explanation 'md' document, with a simple embedded diagram, which concisely explains to the reader how the 'clauditable' file processing sequence works, with explicit explanation of the different file types involved.
```


### Step 2 - Make some improvements to session behaviour

[Step 2 Prompt](initialDistributedSessionsImpls/Step2Prompt.md)

```prompt
In this step we will improve some session behaviour.

In this first increment we will fix the issue where 'new-session "My New Session"' does not strip the quotes.

We should end up with a name 'My New Sessions' -- not '"My New Session"'
```


## Human-Prompt

The flow of the condoc is now within the second step.
