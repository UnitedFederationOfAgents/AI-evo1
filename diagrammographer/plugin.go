package main

import "representable"

// diagrammographer is a plug-in from the start (docs/PluginSubApps.md): it
// serves its own UI manifest and announces itself with a plug-in hello, so
// local-representative reaches it at /apps/dg/. Unlike the sub-apps that
// came before the plug-in break-out it has no legacy /dg/ proxy to fall back
// on -- the frontends frame it only once it is online in the registry. Its
// app id is its --name ("dg" by default): LR only accepts a hello whose app
// id matches the connection's client name.

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="7" height="5" rx="1"/><rect x="15" y="3" width="7" height="5" rx="1"/><rect x="8.5" y="16" width="7" height="5" rx="1"/><path d="M5.5 8v3.5h13V8M12 11.5V16"/></svg>`

// uiManifest is what /ui-manifest.json serves (representable.ServeUIManifest).
// Paths are relative -- see representable.UIManifest. Version 2, like
// composer's: its icon draws in currentColor, so it sets IconMask.
func (s *Server) uiManifest() representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion2,
		AppID:           s.name,
		DisplayName:     "Diagrammographer",
		ShortName:       "DG",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           70,
		Version:         pluginVersion,
		IconMask:        true,
		Description:     "Diagrammographer: diagrams on this host.",
	}
}

// pluginVersion is diagrammographer's manifest Version. Bump it to make
// hosts on different builds show as mismatched in agent-coordinator's global
// view.
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
