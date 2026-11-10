package representable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
)

// Plug-in sub-applications (condocs/privateRepoOpsImpls/Step3Prompt.md): a
// sub-app local-representative doesn't have a hand-written proxy and tab for
// announces itself over its ordinary representable connection, and describes
// its UI in a manifest file it serves itself. The split is deliberate:
//
//   - Existence, address and liveness travel over representable -- one
//     DataPluginHello message after Connect, and the connection itself means
//     "alive". Disconnecting is how a plug-in goes offline.
//   - Everything describing the UI is a versioned file, UIManifestPath on the
//     plug-in's own HTTP server, fetched by local-representative (and
//     reachable by anyone at /apps/<appId>/ui-manifest.json through it). When
//     it changes, the plug-in sends DataManifestChanged and the host re-fetches
//     rather than the manifest itself riding the fire-and-forget data channel.
//
// So representable's vocabulary grows by exactly these two data types. Both
// are ordinary "data" messages, so a mode-mismatched plug-in is refused like
// any other data traffic.
const (
	DataPluginHello     = "plugin-hello"
	DataManifestChanged = "manifest-changed"

	// UIManifestPath is where a plug-in serves its UIManifest, relative to
	// its own HTTP root.
	UIManifestPath = "/ui-manifest.json"

	// PluginIconPath is where this repo's plug-ins serve their manifest icon
	// (see ServeSVG); their manifests name it "." + PluginIconPath. Served
	// from the binary rather than the frontend bundle so it exists in --dev
	// mode too.
	PluginIconPath = "/plugin-icon.svg"

	// PluginHelloVersion / UIManifestVersion are the schema versions this
	// package writes by default. Readers must ignore fields they don't know,
	// so a bump is only needed for a change an old reader would misread.
	PluginHelloVersion = 1
	UIManifestVersion  = 1

	// UIManifestVersion2 adds IconMask and Description (Step3Prompt.md
	// Revision E). A plug-in opts in by serving it, and saying so in its
	// hello; session-manager is the first. A host that only reads version 1
	// skips fetching it (see PluginHello.ManifestVersion) and frames the
	// plug-in without a manifest, so hosts on either version can coexist in
	// one fleet.
	UIManifestVersion2 = 2

	// UIManifestReadVersion is the newest manifest version a host built
	// against this package reads.
	UIManifestReadVersion = UIManifestVersion2
)

// PluginHello is the DataPluginHello payload: who the plug-in is and where
// its HTTP server is. Sent once right after every (re)connect.
type PluginHello struct {
	HelloVersion int    `json:"hello_version"`
	AppID        string `json:"app_id"`       // stable, URL-safe id -- see ValidAppID; must equal the connection's client name
	DisplayName  string `json:"display_name"` // human label until the manifest says otherwise
	HTTPPort     string `json:"http_port"`    // the plug-in's HTTP server, on the host's loopback
	// ManifestVersion is the UIManifest schema version the plug-in serves,
	// so the host can skip fetching one it can't read.
	ManifestVersion int  `json:"manifest_version"`
	DevMode         bool `json:"dev_mode"`
}

// ManifestChanged is the DataManifestChanged payload: the manifest at
// UIManifestPath now has this ETag.
type ManifestChanged struct {
	ETag string `json:"etag"`
}

// UIManifest is what a plug-in serves at UIManifestPath. URLs in it (Entry,
// Icon) are relative to the plug-in's own root, never absolute: the plug-in
// doesn't know the base path it is mounted under (/apps/<appId>/ on
// local-representative, /host/<id>/apps/<appId>/ through agent-coordinator).
type UIManifest struct {
	ManifestVersion int    `json:"manifest_version"`
	AppID           string `json:"app_id"`
	DisplayName     string `json:"display_name"`
	// ShortName is the tab's compact label (e.g. "SM"); optional.
	ShortName string `json:"short_name,omitempty"`
	// Entry is the page a tab embeds, relative to the plug-in root; "" means "./".
	Entry string `json:"entry,omitempty"`
	// Icon is an asset URL inside the plug-in's own tree, relative to its root.
	Icon string `json:"icon,omitempty"`
	// Order is a hint for tab placement (lower first); host config may override it.
	Order int `json:"order,omitempty"`
	// HealthKey names the stateboard app the host reports this plug-in's
	// health under; "" means the AppID.
	HealthKey string `json:"health_key,omitempty"`
	// Version is the plug-in's own build or release, free-form, e.g. "2"
	// (Step3Prompt.md Revision G). It isn't a schema change, so any manifest
	// version may carry it, and a reader that predates it just ignores it.
	// agent-coordinator's global view compares it, with the manifest version
	// and ETag, across hosts to flag a plug-in that differs between them.
	Version string `json:"version,omitempty"`

	// Version 2 (UIManifestVersion2) fields.

	// IconMask says Icon is monochrome, so a host may draw it as a mask in
	// its own text colour rather than as a picture on a light chip. Only
	// read from a version 2 manifest.
	IconMask bool `json:"icon_mask,omitempty"`
	// Description is a sentence about the plug-in, for a tab's tooltip.
	Description string `json:"description,omitempty"`
}

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidAppID reports whether id can be an app id: lowercase, starts with a
// letter, letters/digits/dashes, at most 32 characters -- safe as a single
// URL path segment without escaping.
func ValidAppID(id string) bool { return appIDPattern.MatchString(id) }

// Validate checks the fields a host relies on.
func (h PluginHello) Validate() error {
	if !ValidAppID(h.AppID) {
		return fmt.Errorf("invalid app id %q", h.AppID)
	}
	if h.HTTPPort == "" {
		return fmt.Errorf("plug-in %q sent no http port", h.AppID)
	}
	return nil
}

// SendPluginHello announces this client as a plug-in -- see PluginHello.
// HelloVersion and ManifestVersion are filled in when left zero.
func (c *Client) SendPluginHello(h PluginHello) {
	if h.HelloVersion == 0 {
		h.HelloVersion = PluginHelloVersion
	}
	if h.ManifestVersion == 0 {
		h.ManifestVersion = UIManifestVersion
	}
	// The connection's mode, not the caller's: Connect may have taken it
	// from LaunchModeEnvVar instead.
	h.DevMode = c.mode == ModeDev
	c.SendData(DataPluginHello, h)
}

// SendManifestChanged tells the host to re-fetch this plug-in's UIManifest.
func (c *Client) SendManifestChanged(etag string) {
	c.SendData(DataManifestChanged, ManifestChanged{ETag: etag})
}

// UIManifestBody returns m as served, and the ETag for that body.
func UIManifestBody(m UIManifest) ([]byte, string) {
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return b, `"` + hex.EncodeToString(sum[:8]) + `"`
}

// ServeUIManifest is a plug-in's UIManifestPath handler: it serves
// manifest() with an ETag and answers a matching If-None-Match with 304.
// manifest is called per request, so a manifest that changes is served
// fresh (send SendManifestChanged as well, so the host re-fetches it).
func ServeUIManifest(manifest func() UIManifest) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, etag := UIManifestBody(manifest())
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
}

// ServeSVG serves a fixed SVG document -- a plug-in's PluginIconPath.
func ServeSVG(svg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(svg))
	}
}

// The sandboxed tier (docs/PluginTrust.md): an approved plug-in that isn't
// first-party is served with SandboxCSP on every response and framed with
// IframeSandbox, so its pages run with an opaque origin -- no access to the
// host page, its storage, or (through RefuseSandboxedCallers) the host's own
// APIs. Neither flag set includes allow-same-origin, allow-popups or
// allow-top-navigation.
const (
	SandboxCSP    = "sandbox allow-scripts allow-forms"
	IframeSandbox = "allow-scripts allow-forms" // the frontends' copy of the same flags
)

// FromSandboxedPage reports whether r looks like it was made by a page with
// an opaque origin -- a sandboxed plug-in -- rather than by the host's own
// UI, another program, or the operator navigating:
//   - Origin "null" is what an opaque origin sends on a WebSocket, a CORS
//     fetch and any POST.
//   - Sec-Fetch-Site "cross-site" covers the requests that carry no Origin
//     (an <img>, a plain GET fetch, the frame navigating itself), except a
//     top-level navigation, which is the operator following a link.
//
// Browsers without Sec-Fetch-* headers are only covered by the first rule.
func FromSandboxedPage(r *http.Request) bool {
	if r.Header.Get("Origin") == "null" {
		return true
	}
	return r.Header.Get("Sec-Fetch-Site") == "cross-site" &&
		!(r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document")
}

// RefuseSandboxedCallers wraps a host's whole mux: a request that
// FromSandboxedPage gets 403 unless pluginPath(r.URL.Path) -- the plug-in's
// own tree, where its pages, assets and APIs legitimately live.
func RefuseSandboxedCallers(next http.Handler, pluginPath func(path string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if FromSandboxedPage(r) && !pluginPath(r.URL.Path) {
			http.Error(w, "refused: request from a sandboxed plug-in page", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
