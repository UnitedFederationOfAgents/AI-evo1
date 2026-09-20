# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Next we are implementing the ufa-loader (see 'docs/DevMode.md' for some extra guidance).

The loader will work by being a wrapping executable which launches the inner sub-application. The syntax of launching it will be: 'ufa-loader [optional-loader-flags] <binary> [binary-args]' -- for example 'ufa-loader local-representative --dev-mode'

The sub-application will gain a behaviour where it prints a structured section after an identifying banner as the final act before termination. The loader will recognize this and will trigger a restart. We expect that the binary it calls will have been replaced with a more recent version.

We will implement this ufa-loader sub-application to target only local-representative at first, but it will later be expanded to other sub-applications and so we may think ahead and use a common code approach.

We will introduce a 'make run-loader' command to the makefile of all supporting sub-applications.
