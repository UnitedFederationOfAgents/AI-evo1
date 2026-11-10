package main

import "testing"

// isHostPluginPath is what setupRoutes' RefuseSandboxedCallers exempts: only
// a plug-in's own tree on some host, never AC's own routes or the rest of a
// host's local-representative (docs/PluginTrust.md).
func TestIsHostPluginPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/host/h1/apps/cats/":        true,
		"/host/h1/apps/cats/api/cat": true,
		"/host/h1/api/plugins":       false,
		"/host/h1/ws":                false,
		"/host//apps/cats/":          false,
		"/apps/cats/":                false,
		"/ws":                        false,
		"/host/h1/sessions/apps/x":   false,
	} {
		if got := isHostPluginPath(path); got != want {
			t.Errorf("isHostPluginPath(%q) = %v, want %v", path, got, want)
		}
	}
}
