# Prompt

[AutolaunchChains](../AutolaunchChains.md)

In this step we will ensure all sub-applications can form a connected chain on startup.

In this particular increment we will add a configuration option to federation-command (no config file support yet, only an option to drive it by argument). There will also be a corresponding option to override the port (but it will be the default port by default).

The auto-connect argument will bring FC up in a way where it attempts every 10 seconds for up to the first 10 minutes to connect to LR. It prints on startup that this configuration is selected, and the blinker has a unique brief blue blink in addition to the current mode - an 'accent blink'. The process happens in the background without impacting the foreground activities. If it fails after 10 minutes it will print that this has happened.

Once this increment is complete we will be able to launch FC in one terminal and LR in another and the connection will happen automatically.
