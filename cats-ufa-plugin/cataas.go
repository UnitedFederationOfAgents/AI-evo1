package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Cats-as-a-service (https://cataas.com, Step3Prompt.md Revision G): the
// plug-in's example of calling an outside API. Two calls, both made by the
// plug-in's server when it serves the page, never by the page itself -- a
// sandboxed page's fetches carry no cookies (Revision F), and the server
// running on the host is where an outside call belongs anyway:
//
//   - GET /cat?json=true picks a random cat and answers with its id and tags.
//     The page then shows that cat's picture with an ordinary <img> straight
//     from cats-as-a-service, which a sandboxed page can load.
//   - GET /api/count is how many cats it has, cached for cataasCountTTL.
//
// Neither is required: when cats-as-a-service can't be reached, the page
// says why and still shows its ASCII cat.
const (
	defaultCataasURL = "https://cataas.com"
	cataasTimeout    = 4 * time.Second
	cataasCountTTL   = 10 * time.Minute
	cataasMaxBody    = 64 << 10
	cataasMaxTags    = 8
)

// cataasIDPattern is what a cat id must look like before the page puts it in
// an image URL.
var cataasIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CataasResult is what the page is served about cats-as-a-service.
type CataasResult struct {
	Base     string   `json:"base"` // where the page loads the picture from: Base + "/cat/" + ID
	ID       string   `json:"id,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Mimetype string   `json:"mimetype,omitempty"`
	Count    int      `json:"count,omitempty"` // how many cats it has; 0 if unknown
	Error    string   `json:"error,omitempty"` // why there is no ID
}

type cataas struct {
	base   string
	client *http.Client

	mu      sync.Mutex
	count   int
	countAt time.Time
}

// newCataas returns a client for the cats-as-a-service at base, or nil (no
// calls at all) when base is "".
func newCataas(base string) *cataas {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return nil
	}
	return &cataas{base: base, client: &http.Client{Timeout: cataasTimeout}}
}

// Fetch makes the two calls, concurrently. nil when c is.
func (c *cataas) Fetch(ctx context.Context) *CataasResult {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cataasTimeout)
	defer cancel()

	var count int
	done := make(chan struct{})
	go func() {
		defer close(done)
		count = c.cachedCount(ctx)
	}()

	res := &CataasResult{Base: c.base}
	var cat struct {
		ID       string   `json:"id"`
		LegacyID string   `json:"_id"` // older answers name it this
		Tags     []string `json:"tags"`
		Mimetype string   `json:"mimetype"`
	}
	if err := c.getJSON(ctx, "/cat?json=true", &cat); err != nil {
		res.Error = err.Error()
	} else {
		id := cat.ID
		if id == "" {
			id = cat.LegacyID
		}
		if !cataasIDPattern.MatchString(id) {
			res.Error = fmt.Sprintf("cats-as-a-service answered without a usable cat id (%q)", id)
		} else {
			res.ID, res.Mimetype = id, cat.Mimetype
			if len(cat.Tags) > cataasMaxTags {
				cat.Tags = cat.Tags[:cataasMaxTags]
			}
			res.Tags = cat.Tags
		}
	}
	<-done
	res.Count = count
	return res
}

// cachedCount is GET /api/count, at most once per cataasCountTTL; the last
// good answer (or 0) when the call fails.
func (c *cataas) cachedCount(ctx context.Context) int {
	c.mu.Lock()
	if !c.countAt.IsZero() && time.Since(c.countAt) < cataasCountTTL {
		defer c.mu.Unlock()
		return c.count
	}
	c.mu.Unlock()

	var body struct {
		Count int `json:"count"`
	}
	err := c.getJSON(ctx, "/api/count", &body)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && body.Count > 0 {
		c.count, c.countAt = body.Count, time.Now()
	}
	return c.count
}

func (c *cataas) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cats-as-a-service unreachable from the plug-in: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cats-as-a-service answered %s for %s", resp.Status, path)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, cataasMaxBody)).Decode(v); err != nil {
		return fmt.Errorf("cats-as-a-service answered %s with something that isn't JSON: %v", path, err)
	}
	return nil
}
