# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Next we will implement the 'global' view in agent-coordinator.

Currently agent-coordinator always has a view contextualized on a single host, now we will add a 'global' selection that sits about the list of hosts. This global selection is mutually exclusive with a particular host. When agent-coordinator first displays it defaults to the global view.

When in the global view every tab has an alternative 'global' view (if implemented). During this step we will implement the beginnings of the 'system' tab and mark all others as 'not yet implemented'.

In the implementation of the 'system' tab we will have a set of nested tabs for the selection of the main view. We will have 'topology' and 'timeline' tabs within the system tab, and we will populate topology this increment - we will not make it interactive yet, we will just make it visible.

For guidance on what the topology tab view within the global system tab view looks like we will reference condocs/initialDistributedDevelopmentImpls/global_topology_panel.jpg -- this image is rotated 90 degrees. We have a main pane and a details-and-control right-side pane.

For now we will populate dummy images in the main pane (these will later represent our hosts) and we will populate dummy readouts in the details-and-control pane.
