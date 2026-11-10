package main

import "representable"

// composer is a plug-in (condocs/privateRepoOpsImpls/Step3Prompt.md Revision
// B): it serves its own UI manifest and announces itself with a plug-in
// hello, so local-representative reaches it at /apps/compose/. LR's legacy
// /compose/ proxy stays as the frontends' fallback until every host runs a
// build that says hello. Its app id is its --name ("compose" by default): LR
// only accepts a hello whose app id matches the connection's client name.

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 10h18M8 5v5M14 5v5"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest.
//
// composer was bumped to manifest version 2 with its own Version
// (Step3Prompt.md Revision H), so a host still on an older build trips
// agent-coordinator's version-mismatch mark on the compose tab. Its icon
// draws in currentColor, so it sets IconMask.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion2,
		AppID:           s.name,
		DisplayName:     "Composer",
		ShortName:       "CP",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           60,
		Version:         pluginVersion,
		IconMask:        true,
		Description:     "Composer: build and render compositions on this host.",
	}
}

// pluginVersion is composer's manifest Version. Bump it to make hosts on
// different builds show as mismatched in agent-coordinator's global view.
const pluginVersion = "2"

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
