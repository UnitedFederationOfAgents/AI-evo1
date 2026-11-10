package main

import "representable"

// condocv2 is a plug-in (condocs/privateRepoOpsImpls/Step3Prompt.md
// Revision B): it serves its own UI manifest and announces itself with a
// plug-in hello, so local-representative reaches it at /apps/condocv2/.
// LR's legacy /condocv2/ proxy stays as the frontends' fallback until every
// host runs a build that says hello. Its app id is its --name: LR only
// accepts a hello whose app id matches the connection's client name.

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/><circle cx="9" cy="15" r="2"/><path d="M11 15h4"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion,
		AppID:           s.name,
		DisplayName:     "Condoc v2",
		ShortName:       "C2",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           20,
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
