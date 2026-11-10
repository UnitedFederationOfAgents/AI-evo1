package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLR serves a files tab's GET /api/files, /api/files/<id> and
// /api/file-peers, as local-representative does.
func fakeLR(t *testing.T, files map[string]string, highlighted map[string]bool, peers func() []filePeer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/files":
			var list []map[string]interface{}
			for id, data := range files {
				list = append(list, map[string]interface{}{"id": id, "name": hostFileName(id), "size": len(data), "highlighted": highlighted[id]})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"files": list})
		case r.URL.Path == "/api/file-peers":
			if peers == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"peers": peers()})
		case strings.HasPrefix(r.URL.Path, "/api/files/"):
			data, ok := files[strings.TrimPrefix(r.URL.Path, "/api/files/")]
			if !ok || r.URL.Query().Get("download") != "1" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(data))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHighlightedResourceFiles(t *testing.T) {
	peer := fakeLR(t, map[string]string{"0000beef_c.txt": "peer's", "0000cafe_d.txt": "plain"},
		map[string]bool{"0000beef_c.txt": true}, nil)
	lr := fakeLR(t, map[string]string{"0123abcd_a.txt": "mine", "89abcdef_b.txt": "not highlighted"},
		map[string]bool{"0123abcd_a.txt": true},
		func() []filePeer { return []filePeer{{Node: "pi", Base: peer.URL}, {Node: "gone", Base: "http://127.0.0.1:1"}} })

	listed, err := listAllHighlighted(lr.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Key != "0123abcd_a.txt" || listed[0].Name != "a.txt" ||
		listed[1].Key != "pi/0000beef_c.txt" || listed[1].Node != "pi" {
		t.Fatalf("listed %+v: want this LR's a.txt, then pi's c.txt (the unreachable peer skipped)", listed)
	}

	// None picked: all of them.
	files, err := highlightedResourceFiles(lr.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].name != "a.txt" || string(files[0].data) != "mine" || string(files[1].data) != "peer's" {
		t.Errorf("all highlighted read as %+v", files)
	}

	// Picked by key.
	files, err = highlightedResourceFiles(lr.URL, []string{"pi/0000beef_c.txt"})
	if err != nil || len(files) != 1 || files[0].name != "c.txt" {
		t.Errorf("picked read as %+v, %v", files, err)
	}

	// A file not (or no longer) highlighted is refused.
	for _, key := range []string{"89abcdef_b.txt", "pi/0000cafe_d.txt", "pi/0123abcd_a.txt"} {
		if _, err := highlightedResourceFiles(lr.URL, []string{key}); err == nil {
			t.Errorf("%s was taken", key)
		}
	}

	// An LR from before GET /api/file-peers, with nothing highlighted.
	bare := fakeLR(t, map[string]string{"0123abcd_a.txt": "mine"}, nil, nil)
	if got, err := listAllHighlighted(bare.URL); err != nil || len(got) != 0 {
		t.Errorf("bare LR listed %+v, %v", got, err)
	}
	if _, err := highlightedResourceFiles(bare.URL, nil); err == nil || !strings.Contains(err.Error(), "highlight some") {
		t.Errorf("nothing highlighted: %v", err)
	}
	if _, err := listAllHighlighted("http://127.0.0.1:1"); err == nil {
		t.Error("an unreachable LR listed nothing, without an error")
	}
}
