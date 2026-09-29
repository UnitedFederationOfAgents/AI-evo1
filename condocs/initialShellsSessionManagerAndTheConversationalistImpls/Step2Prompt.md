# Prompt

[InitialShellsSessionManagerAndTheConversationalist](../InitialShellsSessionManagerAndTheConversationalist.md)

Now let's add a piece of cross-app integration to allow voice input into condoccer input fields.

Condoccer will use the CO<-->LR<-->AC chain to poll for availability of TC. When one or more TC instance is available (on any host) it will recognize TC as available.

We will show when we have TC availability with a mic icon beside the camera icon in our AGENT-COORDINATOR bar. (Illuminated when available)

Whenever we have a text input field for condoccer and it sees TC is available we will see a 'mic' icon become visible. Pressing this icon will trigger the conversationalist's ability to transcribe text and will populate it into the text field.

We will keep the TC functionality as contained in that sub-app as we can, we only want the text to get into other text boxes.
