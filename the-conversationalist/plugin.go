package main

import "representable"

// the-conversationalist is a plug-in (condocs/privateRepoOpsImpls/
// Step3Prompt.md Revision B): it serves its own UI manifest and announces
// itself with a plug-in hello, so local-representative reaches it at
// /apps/convo/. LR's legacy /convo/ proxy stays as the frontends' fallback
// until every host runs a build that says hello. Its app id is its --name
// ("convo" by default): LR only accepts a hello whose app id matches the
// connection's client name.

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3M8 21h8"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion,
		AppID:           s.name,
		DisplayName:     "The Conversationalist",
		ShortName:       "TC",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           30,
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
	client.SendPluginHello(representable.PluginHello{
		AppID:       s.name,
		DisplayName: s.uiManifest().DisplayName,
		HTTPPort:    s.httpPort,
		DevMode:     s.devMode,
	})
}
