package slopspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Browsing a slopspace's files, for the worker tab's "view files"
// (dTT-IntegrationImpls Step 1, Revision H). It looks wherever the spaces
// are right now -- the deploy location while deployed, else the slopspace's
// own directory -- and only inside readspaces/ and writespaces/. Nothing is
// written, so it is safe while an agent is working in the slopspace.

// Limits on one Browse answer, which travels back as one representable line.
const (
	MaxBrowseEntries = 400
	MaxBrowseContent = 24 * 1024
)

// FileEntry is one entry of a browsed directory.
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// FileListing is what Browse found at a path: a directory's entries, or a
// file's content (its start, when long; nothing, when it isn't text).
type FileListing struct {
	Path      string      `json:"path"` // relative to the slopspace's spaces, e.g. "writespaces/dtt-requests"
	Root      string      `json:"root"` // where the spaces are right now
	Dir       bool        `json:"dir"`
	Entries   []FileEntry `json:"entries,omitempty"`
	Omitted   int         `json:"omitted,omitempty"` // entries past MaxBrowseEntries
	Size      int64       `json:"size,omitempty"`
	Content   string      `json:"content,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Binary    bool        `json:"binary,omitempty"`
}

// Browse lists a directory or reads a file of a slopspace. rel is relative to
// where its spaces are; "" lists the two spaces themselves.
func (m *Manager) Browse(id, rel string) (*FileListing, error) {
	metadata, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	base := metadata.RootPath
	if metadata.Deployed && metadata.DeployPath != "" {
		base = metadata.DeployPath
	}

	rel = strings.Trim(path.Clean("/"+filepath.ToSlash(strings.TrimSpace(rel))), "/")
	out := &FileListing{Path: rel, Root: base}
	if rel == "" {
		out.Dir = true
		for _, d := range []string{ReadSpacesDir, WriteSpacesDir} {
			if fi, err := os.Stat(filepath.Join(base, d)); err == nil && fi.IsDir() {
				out.Entries = append(out.Entries, FileEntry{Name: d, Dir: true})
			}
		}
		return out, nil
	}
	top, _, _ := strings.Cut(rel, "/")
	if top != ReadSpacesDir && top != WriteSpacesDir {
		return nil, fmt.Errorf("only %s/ and %s/ can be browsed", ReadSpacesDir, WriteSpacesDir)
	}

	full := filepath.Join(base, filepath.FromSlash(rel))
	// A symlink in the spaces mustn't lead out of them.
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	inside := false
	for _, d := range []string{ReadSpacesDir, WriteSpacesDir} {
		space := filepath.Join(realBase, d)
		if real == space || strings.HasPrefix(real, space+string(filepath.Separator)) {
			inside = true
		}
	}
	if !inside {
		return nil, fmt.Errorf("%s leads outside the slopspace's spaces", rel)
	}

	fi, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		out.Dir = true
		entries, err := os.ReadDir(real)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			fe := FileEntry{Name: e.Name(), Dir: e.IsDir()}
			if info, err := e.Info(); err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					if t, err := os.Stat(filepath.Join(real, e.Name())); err == nil {
						info = t
						fe.Dir = t.IsDir()
					}
				}
				if !fe.Dir {
					fe.Size = info.Size()
				}
			}
			out.Entries = append(out.Entries, fe)
		}
		// Directories first, then by name.
		sort.SliceStable(out.Entries, func(i, j int) bool {
			if out.Entries[i].Dir != out.Entries[j].Dir {
				return out.Entries[i].Dir
			}
			return out.Entries[i].Name < out.Entries[j].Name
		})
		if len(out.Entries) > MaxBrowseEntries {
			out.Omitted = len(out.Entries) - MaxBrowseEntries
			out.Entries = out.Entries[:MaxBrowseEntries]
		}
		return out, nil
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a regular file", rel)
	}

	out.Size = fi.Size()
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, MaxBrowseContent)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:n]
	out.Truncated = fi.Size() > int64(n)
	if out.Truncated {
		// Don't end on half a rune.
		for len(buf) > 0 && !utf8.Valid(buf) && len(buf) > n-utf8.UTFMax {
			buf = buf[:len(buf)-1]
		}
	}
	if !utf8.Valid(buf) || strings.IndexByte(string(buf), 0) >= 0 {
		out.Binary = true
		return out, nil
	}
	out.Content = string(buf)
	return out, nil
}

// ShortenContent halves a listing's content (or entries), for a caller that
// has to fit it into a size limit. It reports false when there's nothing left
// to cut.
func (l *FileListing) ShortenContent() bool {
	switch {
	case len(l.Content) > 256:
		c := l.Content[:len(l.Content)/2]
		for len(c) > 0 && !utf8.ValidString(c) {
			c = c[:len(c)-1]
		}
		l.Content = c
		l.Truncated = true
		return true
	case len(l.Entries) > 16:
		half := len(l.Entries) / 2
		l.Omitted += len(l.Entries) - half
		l.Entries = l.Entries[:half]
		return true
	}
	return false
}
