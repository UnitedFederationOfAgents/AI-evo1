# Prompt

[Step1Prompt](Step1Prompt.md)

Let's add some more functionality to the system dialogs.

We will add a bar of icons above the application status boxes in the global system view - they will only appear if they are present (as opposed to the illuminated vs non-illuminated of the application status boxes). These icons will represent connectivity. So far we will have an AWS icon which is present if we have AWS creds, and a github icon which is present if we have github PAT. If these credentials are not working then we will see a yellow border.

When we are in the per-host system dialog we will see a dialog ('providers') between the applications/launch area and the trough notifications area. This new area will have vertically spaced sections displaying the status and principal name for each of the connections. The 'connections' dialog will always be present and will show even if empty, but each individual connection section will be created only if present.

We will now also add double-click navigation. If we double click on a host box on the global system dialog it will bring us to that host's system dialog. If we double-click on a sub-app box within a host box in the global system dialog we will instead go to that sub-app tab for that host.

When we are in the per-host dialog we may now double-click on a sub-app row in the sub-app management dialog and it will take us to that tab.
