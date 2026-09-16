# Prompt

[InitialFileExchange](../InitialFileExchange.md)

Let's add the following controls to the file details dialog:
- delete: has a confirm dialog, deletes the file
- hold/persist: the button originally appears as 'hold', when pressed the file's manifest is updated to mark the file for 72 hour cache. If a file is held then the button 'persist' appears in place of hold. Pressing 'persist' moves the file to the host-store (/host-agent-files/exchange/host-store by default) where it will not be automatically deleted.

The file icons should be updates to wireframe icons - when a file is cached short-term it appears as orange. When a file is held it is yellow. When a file is persisted it turns green.
