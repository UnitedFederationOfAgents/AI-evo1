package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"net/http"
	"strings"
)

// GIF export. The browser draws the diagram (each step of a dynamic one) to
// PNGs, as it does for the PNG export, and sends them here; Go's image/gif
// puts them together, which spares the frontend a GIF encoder.

// GIFRequest is POST /api/export/gif's body.
type GIFRequest struct {
	Frames  []string `json:"frames"`   // PNG data URLs (data:image/png;base64,...), in order
	DelayMS int      `json:"delay_ms"` // how long each frame shows; 1500 when 0
}

const (
	maxGIFBody   = 64 << 20
	maxGIFFrames = 100
)

func (s *Server) handleExportGIF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req GIFRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGIFBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reading the request: %v", err))
		return
	}
	out, err := encodeGIF(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Write(out)
}

// encodeGIF turns the request's PNG frames into a looping GIF.
func encodeGIF(req GIFRequest) ([]byte, error) {
	if len(req.Frames) == 0 || len(req.Frames) > maxGIFFrames {
		return nil, fmt.Errorf("send 1 to %d frames (got %d)", maxGIFFrames, len(req.Frames))
	}
	delay := req.DelayMS
	if delay <= 0 {
		delay = 1500
	}
	anim := &gif.GIF{}
	for i, f := range req.Frames {
		_, b64, ok := strings.Cut(f, ";base64,")
		if !ok {
			return nil, fmt.Errorf("frame %d: expected a base64 PNG data URL", i+1)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %v", i+1, err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("frame %d: %v", i+1, err)
		}
		pal := image.NewPaletted(img.Bounds(), palette.Plan9)
		draw.FloydSteinberg.Draw(pal, img.Bounds(), img, img.Bounds().Min)
		anim.Image = append(anim.Image, pal)
		anim.Delay = append(anim.Delay, delay/10) // GIF delays are in 100ths of a second
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
