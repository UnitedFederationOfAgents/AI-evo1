# Prompt

[SlopspaceCondocs](../SlopspaceCondocs.md)

We will start by putting the condocs implementation we have so far under a new 'same-repo' tab and adding a 'slopspace' tab.

The slopspace tab will not yet be populated.

On our 'same-repo' tab we will also introduce a filter (enabled by default) to 'Hide Completed'.


prompt: [`3bb3382`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3bb3382518899b5cafc585183df47efb6f3b58f2) → reply: [`ff34c50`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ff34c5093be27defa897d430c13074fe21e877e8)

## Reply

I added a Same-Repo / Slopspace tab bar to the top of the condoccer condoc list. All the existing behaviour now sits under Same-Repo, and that tab has a "Hide Completed" checkbox that is on by default. I couldn't build or type-check it: `npm run build` and `tsc` both needed approval that wasn't given. So I've only read through the diff by hand, and nothing has been run in a browser.

- **Same-Repo tab:** the condoc list now leaves out completed condocs unless the box is unticked. Next to the checkbox it shows how many are hidden, and if every condoc is completed it says "All condocs are completed." The checkbox setting is remembered across reloads in the browser's local storage.
- **Slopspace tab:** deliberately empty for now. The sidebar and main pane just say the mode isn't supported yet. Selecting this tab sets the page URL to `#/slopspace`, so a refresh comes back to it.

The tab bar only appears on the top-level list. Once you open a condoc you're in the same-repo views, as before. The changes are in `condoccer/frontend/src/App.tsx` and `index.css`, and are left uncommitted.


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
