// cats-ufa-plugin is the hello-world external plug-in
// (condocs/privateRepoOpsImpls/Step3Prompt.md Revision C): the smallest
// program that does everything docs/PluginSubApps.md asks of a plug-in, and
// that runs correctly in the sandboxed tier docs/PluginTrust.md puts every
// external plug-in in. Copy it to start a new one.
//
// It is deliberately not one of local-representative's first-party
// sub-apps: LR lists it as "pending" in its plugins tab until an operator
// approves it there, and only launches it when asked to by that tab's
// "register example" (Revision E), which approves it too.
//
//	cats-ufa-plugin --port 8090 --lr-port 8082
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net"
	"net/http"
	"time"

	"representable"
)

//go:embed static
var static embed.FS

const reconnectInterval = 5 * time.Second

// iconSVG is the manifest icon, served at representable.PluginIconPath. Its
// colours are its own: the host shows it as an <img>, where currentColor
// would be black.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4 3l4 5h8l4-5v10a8 8 0 0 1-16 0z" fill="#f0a04b" stroke="#7a4a1a" stroke-width="1.2" stroke-linejoin="round"/><circle cx="9" cy="12" r="1.3" fill="#2b2b2b"/><circle cx="15" cy="12" r="1.3" fill="#2b2b2b"/><path d="M11 15h2l-1 1.2z" fill="#c0506a"/><path d="M3 15h4M3 17h4M17 15h4M17 17h4" stroke="#7a4a1a" stroke-width=".8"/></svg>`

// pluginVersion is this plug-in's own version, sent as the manifest's
// Version so agent-coordinator can tell hosts running different builds of it
// apart (Step3Prompt.md Revision G). "2" is the first build that calls
// cats-as-a-service.
const pluginVersion = "2"

// Cat is what GET /api/cat answers. The page is served with one too, which
// also carries Cataas when the page handler reached cats-as-a-service.
type Cat struct {
	N    int    `json:"n"`
	Art  string `json:"art"`
	Fact string `json:"fact"`

	Cataas *CataasResult `json:"cataas,omitempty"`
}

var cats = []string{
	" /\\_/\\\n( o.o )\n > ^ <",
	" /\\_/\\\n( -.- )\n o(\")(\")",
	"  /\\_/\\  \n ( ^.^ ) \n  )   (  \n (__|__) ",
	" |\\__/,|   (`\\\n |_ _  |.--.) )\n ( T   )     /\n(((^_(((/(((_/",
}

var facts = []string{
	"Cats sleep for around 12 to 16 hours a day.",
	"A group of cats is called a clowder.",
	"Cats can rotate their ears about 180 degrees.",
	"A cat's nose print is as unique as a human fingerprint.",
	"Cats walk like camels and giraffes: both right feet, then both left feet.",
	"This page has no origin of its own: it is a sandboxed plug-in.",
}

func main() {
	port := flag.String("port", "8090", "HTTP port to listen on")
	name := flag.String("name", "cats-ufa-plugin", "app id, and the name reported to local-representative: they must match")
	lrHost := flag.String("lr-host", "localhost", "local-representative host/IP")
	lrPort := flag.String("lr-port", "8082", "local-representative representable port")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md); must match local-representative's")
	cataasURL := flag.String("cataas-url", defaultCataasURL, "cats-as-a-service base URL (see cataas.go); \"\" to never call it")
	flag.Parse()

	if !representable.ValidAppID(*name) {
		log.Fatalf("--name %q is not a valid app id (see representable.ValidAppID)", *name)
	}
	go connectLoop(net.JoinHostPort(*lrHost, *lrPort), *name, *port, *devMode)

	addr := ":" + *port
	log.Printf("cats-ufa-plugin %q listening on http://localhost%s", *name, addr)
	log.Fatal(http.ListenAndServe(addr, routes(*name, newCataas(*cataasURL))))
}

// manifest is what /ui-manifest.json serves. Every URL is relative to this
// plug-in's own root: it doesn't know the base path it is mounted under.
func manifest(appID string) representable.UIManifest {
	return representable.UIManifest{
		ManifestVersion: representable.UIManifestVersion,
		AppID:           appID,
		DisplayName:     "Cats",
		ShortName:       "Cats",
		Entry:           "./",
		Icon:            "." + representable.PluginIconPath,
		Order:           100,
		Version:         pluginVersion,
	}
}

func routes(appID string, cs *cataas) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(representable.UIManifestPath, representable.ServeUIManifest(func() representable.UIManifest { return manifest(appID) }))
	mux.HandleFunc(representable.PluginIconPath, representable.ServeSVG(iconSVG))
	mux.HandleFunc("/api/cat", handleCat)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		page, _ := static.ReadFile("static/index.html")
		c := randomCat()
		c.Cataas = cs.Fetch(r.Context())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store") // "another cat" reloads the page
		w.Write(withCat(page, c))
	})
	return mux
}

// catPlaceholder is where index.html takes the cat it is served with.
const catPlaceholder = "/*CAT*/null"

// withCat fills page's catPlaceholder in with c. The page gets its cat this
// way rather than fetching /api/cat (Step3Prompt.md Revision F): a sandboxed
// page's fetches carry no cookies, so behind a cookie login in front of the
// host they never arrive, while the frame's own navigation does.
// json.Marshal escapes <, > and &, so the result is safe inside <script>.
func withCat(page []byte, c Cat) []byte {
	js, err := json.Marshal(c)
	if err != nil {
		return page
	}
	return bytes.Replace(page, []byte(catPlaceholder), js, 1)
}

func randomCat() Cat {
	n := rand.Intn(len(cats) * len(facts))
	return Cat{N: n, Art: cats[n%len(cats)], Fact: facts[n/len(cats)]}
}

// handleCat answers GET /api/cat with a random cat. In the sandboxed tier
// the page calling it has an opaque origin, so even this plug-in's own API
// is cross-origin to it: without Access-Control-Allow-Origin the browser
// would hide the answer. "*" is right for an API with nothing private in
// it -- the sandbox sends no cookies anyway.
func handleCat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(randomCat())
}

// connectLoop keeps a representable connection to local-representative,
// sending the plug-in hello after every (re)connect. The connection is the
// liveness signal: while it is down LR shows this plug-in offline.
func connectLoop(addr, appID, httpPort string, devMode bool) {
	for {
		c, err := representable.Connect(addr, appID, representable.Mode(devMode), 3*time.Second)
		if err != nil {
			time.Sleep(reconnectInterval)
			continue
		}
		log.Printf("connected to local-representative at %s as %q", addr, appID)
		c.SendPluginHello(representable.PluginHello{
			AppID:       appID,
			DisplayName: manifest(appID).DisplayName,
			HTTPPort:    httpPort,
			DevMode:     devMode,
		})
		<-c.DisconnectCh()
		log.Printf("disconnected from local-representative at %s; retrying every %s", addr, reconnectInterval)
		time.Sleep(reconnectInterval)
	}
}
