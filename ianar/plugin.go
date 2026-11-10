package main

import "representable"

// ianar is a plug-in (condocs/privateRepoOpsImpls/Step3Prompt.md Revision
// B): it serves its own UI manifest and announces itself with a plug-in
// hello, so local-representative reaches it at /apps/robot/. LR's legacy
// /robot/ proxy stays as the frontends' fallback until every host runs a
// build that says hello. Its app id is its --name ("robot" by default): LR
// only accepts a hello whose app id matches the connection's client name.

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="8" width="14" height="11" rx="2"/><path d="M12 4v4M9 13h.01M15 13h.01M10 16h4"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion,
		AppID:           s.name,
		DisplayName:     "Robot",
		ShortName:       "RB",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           50,
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
