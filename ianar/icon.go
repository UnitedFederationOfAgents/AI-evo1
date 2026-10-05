package main

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file finds an app's icon on screen by its picture, for the
// sequence-v2 "click-icon" op (seqv2_ops.go) -- e.g. Firefox's icon in the
// dock, which has no text for OCR to find.
//
// The icon is looked up the way the desktop does: the app's .desktop file
// names it (Icon=), as an absolute path (snaps) or a name to find in the
// icon themes. Only PNG icons can be matched (SVG would need a renderer).
//
// Matching is a template search over the capture: the icon is scaled to
// each plausible on-screen size, and compared, over its opaque pixels only
// (so the dock's background behind it doesn't matter), at every position --
// first coarsely, on a grid and a sample of pixels, then exactly around the
// best coarse spots. The score is the mean per-channel difference (0-255);
// the best placement counts as found if it is at most iconMaxScore.

const (
	iconMaxScore     = 40.0 // mean per-channel difference (0-255) accepted as a match
	iconSamplePoints = 96   // opaque pixels compared per position in the coarse pass
	iconRefineKeep   = 4    // coarse candidates refined per size
)

// iconSizes are the on-screen sizes tried, in capture pixels: dock and app
// grid icons at scale 1 and 2.
var iconSizes = []int{24, 28, 32, 36, 40, 44, 48, 52, 56, 64, 72, 80, 88, 96, 104, 112, 128, 144, 160, 192}

// iconDataDirs are the XDG data directories holding applications/ and
// icons/, best first. Overridable in tests.
var iconDataDirs = func() []string {
	home := homeDir()
	dirs := []string{}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dirs = append(dirs, d)
	} else {
		dirs = append(dirs, filepath.Join(home, ".local/share"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, filepath.SplitList(dataDirs)...)
	return append(dirs,
		"/var/lib/snapd/desktop",
		"/var/lib/flatpak/exports/share",
		filepath.Join(home, ".local/share/flatpak/exports/share"),
	)
}

// appIconFile finds the PNG icon for app (a .desktop file id such as
// "firefox" or "org.gnome.TextEditor"; snap and flatpak ids that contain it,
// like "firefox_firefox", also count).
func appIconFile(app string) (string, error) {
	app = strings.TrimSuffix(strings.TrimSpace(app), ".desktop")
	desktop, err := findDesktopFile(app)
	if err != nil {
		return "", err
	}
	icon, err := desktopIconName(desktop)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(icon) {
		if strings.HasSuffix(icon, ".png") {
			return icon, nil
		}
		return "", fmt.Errorf("%s's icon %s isn't a PNG; give click-icon a PNG of it as icon", app, icon)
	}
	if p := themeIconFile(icon); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("found no PNG for %s's icon %q (from %s) in the icon themes; give click-icon a PNG of it as icon", app, icon, desktop)
}

// findDesktopFile returns app's .desktop file: an exact id match first, then
// one whose id contains app.
func findDesktopFile(app string) (string, error) {
	var loose []string
	for _, d := range iconDataDirs() {
		dir := filepath.Join(d, "applications")
		exact := filepath.Join(dir, app+".desktop")
		if _, err := os.Stat(exact); err == nil {
			return exact, nil
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".desktop") && strings.Contains(name, strings.ToLower(app)) {
				loose = append(loose, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(loose) == 0 {
		return "", fmt.Errorf("no .desktop file for %q in the applications folders", app)
	}
	// The shortest id is the likeliest: firefox_firefox over
	// firefox-developer-edition.
	sort.SliceStable(loose, func(i, j int) bool { return len(filepath.Base(loose[i])) < len(filepath.Base(loose[j])) })
	return loose[0], nil
}

// desktopIconName reads Icon= from a .desktop file's [Desktop Entry].
func desktopIconName(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inEntry := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if v, ok := strings.CutPrefix(line, "Icon="); ok && inEntry {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("%s names no icon", path)
}

// themeIconFile finds the largest PNG for icon name in the hicolor theme
// (where apps install their icons) or pixmaps.
func themeIconFile(name string) string {
	sizes := []string{"512x512", "256x256", "192x192", "128x128", "96x96", "64x64", "48x48", "32x32"}
	for _, d := range iconDataDirs() {
		for _, sz := range sizes {
			p := filepath.Join(d, "icons", "hicolor", sz, "apps", name+".png")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	for _, d := range append(iconDataDirs(), "/usr/share") {
		p := filepath.Join(d, "pixmaps", name+".png")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func loadIconImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the icon: %w", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("reading the icon %s: %w", path, err)
	}
	return img, nil
}

// rgbImage is an image as packed 8-bit RGB, for fast comparison.
type rgbImage struct {
	w, h int
	pix  []uint8 // 3 bytes per pixel, row by row
}

func toRGB(img image.Image) rgbImage {
	b := img.Bounds()
	out := rgbImage{w: b.Dx(), h: b.Dy(), pix: make([]uint8, b.Dx()*b.Dy()*3)}
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			out.pix[i], out.pix[i+1], out.pix[i+2] = c.R, c.G, c.B
			i += 3
		}
	}
	return out
}

// iconPixel is one opaque pixel of a scaled icon.
type iconPixel struct {
	dx, dy  int
	r, g, b int32
}

// scaleIcon resamples icon to size x size by area averaging (with alpha
// weighting) and returns its opaque pixels.
func scaleIcon(icon image.Image, size int) []iconPixel {
	b := icon.Bounds()
	sw, sh := b.Dx(), b.Dy()
	var out []iconPixel
	for ty := 0; ty < size; ty++ {
		y0, y1 := ty*sh/size, max((ty+1)*sh/size, ty*sh/size+1)
		for tx := 0; tx < size; tx++ {
			x0, x1 := tx*sw/size, max((tx+1)*sw/size, tx*sw/size+1)
			var r, g, bl, a, n int64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := color.NRGBAModel.Convert(icon.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
					w := int64(c.A)
					r += int64(c.R) * w
					g += int64(c.G) * w
					bl += int64(c.B) * w
					a += w
					n++
				}
			}
			// Only (nearly) opaque pixels are compared: edges blend with
			// whatever is behind the icon.
			if n == 0 || a < 230*n {
				continue
			}
			out = append(out, iconPixel{tx, ty, int32(r / a), int32(g / a), int32(bl / a)})
		}
	}
	return out
}

// iconMatch is the best placement found for an icon.
type iconMatch struct {
	found bool
	rect  image.Rectangle // where on the capture (also set for the best near miss)
	score float64
}

// locateIcon searches img for icon at each of iconSizes.
func locateIcon(img image.Image, icon image.Image) iconMatch {
	scr := toRGB(img)
	best := iconMatch{score: 1e9}
	for _, size := range iconSizes {
		if size > scr.w || size > scr.h {
			continue
		}
		px := scaleIcon(icon, size)
		if len(px) < 16 {
			continue
		}
		step := max(1, len(px)/iconSamplePoints)
		var sample []iconPixel
		for i := 0; i < len(px); i += step {
			sample = append(sample, px[i])
		}
		grid := max(2, size/10)

		type cand struct {
			x, y  int
			score float64
		}
		var top []cand
		limit := iconMaxScore * 2 // coarse placements worse than this aren't worth refining
		for y := 0; y+size <= scr.h; y += grid {
			for x := 0; x+size <= scr.w; x += grid {
				s := iconScore(scr, sample, x, y, limit)
				if s > limit {
					continue
				}
				top = append(top, cand{x, y, s})
				sort.Slice(top, func(i, j int) bool { return top[i].score < top[j].score })
				if len(top) > iconRefineKeep {
					top = top[:iconRefineKeep]
				}
				if len(top) == iconRefineKeep {
					limit = top[len(top)-1].score
				}
			}
		}
		for _, c := range top {
			for y := max(0, c.y-grid); y <= min(scr.h-size, c.y+grid); y++ {
				for x := max(0, c.x-grid); x <= min(scr.w-size, c.x+grid); x++ {
					if s := iconScore(scr, px, x, y, best.score); s < best.score {
						best = iconMatch{rect: image.Rect(x, y, x+size, y+size), score: s}
					}
				}
			}
		}
	}
	best.found = !best.rect.Empty() && best.score <= iconMaxScore
	return best
}

// iconScore is the mean per-channel difference between px placed at (x, y)
// and the screen, giving up early (returning something above limit) once it
// can no longer come in under limit.
func iconScore(scr rgbImage, px []iconPixel, x, y int, limit float64) float64 {
	budget := int64(limit*3*float64(len(px))) + 1
	var sum int64
	for _, p := range px {
		i := ((y+p.dy)*scr.w + x + p.dx) * 3
		sum += absDiff(int32(scr.pix[i]), p.r) + absDiff(int32(scr.pix[i+1]), p.g) + absDiff(int32(scr.pix[i+2]), p.b)
		if sum > budget {
			return limit + 1
		}
	}
	return float64(sum) / float64(3*len(px))
}

func absDiff(a, b int32) int64 {
	if a > b {
		return int64(a - b)
	}
	return int64(b - a)
}
