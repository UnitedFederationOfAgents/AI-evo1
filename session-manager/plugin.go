package main

import "representable"

// session-manager is a plug-in (condocs/privateRepoOpsImpls/Step3Prompt.md):
// it serves its own UI manifest and announces itself with a plug-in hello, so
// local-representative reaches it at /apps/sessions/. LR's legacy /sessions/
// proxy stays as the frontends' fallback until every host runs a build that
// says hello.

// session-manager's app id is its --name ("sessions" by default):
// local-representative only accepts a hello whose app id matches the
// connection's client name (see its plugins.go).

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9h10M7 13h10M7 17h6"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest.
//
// session-manager is the one sub-app serving manifest version 2
// (Step3Prompt.md Revision E), so a fleet whose hosts run different builds
// shows both versions side by side; the other sub-apps stay on version 1.
// Its icon draws in currentColor, so it sets IconMask.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion2,
		AppID:           s.name,
		DisplayName:     "Sessions",
		ShortName:       "SM",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           40,
		IconMask:        true,
		Description:     "Session-manager: this host's agent sessions and their records.",
	}
}

// sendPluginHello announces this instance as a plug-in to
// local-representative. No-op when not connected.
func (s *Server) sendPluginHello() {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		return
	}
	m := s.uiManifest()
	client.SendPluginHello(representable.PluginHello{
		AppID:           s.name,
		DisplayName:     m.DisplayName,
		HTTPPort:        s.httpPort,
		ManifestVersion: m.ManifestVersion,
		DevMode:         s.devMode,
	})
}
