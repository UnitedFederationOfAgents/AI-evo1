package slopspace

// Read-only repos (sloppo privateRepoOpsImpls Step 6, Revision A): a
// slopspace can carry repos the agent reads but never changes, at
// readspaces/repos/<owner>/<name>. Each is registered in the slopspace's
// metadata (types.ReadspaceRepo) with the ref it follows and the shared clone
// it comes from (pkg/readspace), and is exported afresh -- fetched, its ref
// resolved, the commit written out without .git -- on every Deploy and Return:
//
//   - on Deploy, so each new piece of work starts from the ref's latest commit
//   - on Return, because the deployed readspaces are thrown away, and with them
//     anything the agent changed in its copy
//
// The copy's files and directories are made read-only, so a change fails
// rather than being silently lost. Every new commit a ref resolves to is
// appended to the repo's Resolutions, so which commit each deployment saw
// stays known.

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
	"dungeon-keeper/pkg/types"
)

// What resolved a read-only repo's commit (types.ReadspaceResolution.Event).
const (
	ReadspaceEventAdd    = "add"
	ReadspaceEventDeploy = "deploy"
	ReadspaceEventReturn = "return"
)

// AddReadspaceRepo registers owner/repo as a read-only repo of the slopspace,
// following ref (a branch, tag or commit; "" follows the default branch), and
// exports it into readspaces/repos from sourceRepoPath, a clone of it (see
// pkg/readspace, whose clone is kept current by its own clone/update). Adding
// a repo again changes the ref it follows. It returns the repo as registered.
func (m *Manager) AddReadspaceRepo(id string, owner, repo string, ref string, sourceRepoPath string) (*types.ReadspaceRepo, error) {
	metadata, err := m.Get(id)
	if err != nil {
		return nil, err
	}

	if metadata.Deployed {
		return nil, fmt.Errorf("cannot add readspace repo while deployed")
	}

	ownerRepo := owner + "/" + repo
	entry := types.ReadspaceRepo{Repo: ownerRepo, Ref: strings.TrimSpace(ref), Source: sourceRepoPath}
	at := -1
	for i, r := range metadata.ReadspaceRepos {
		if strings.EqualFold(r.Repo, ownerRepo) {
			at = i
			entry.SHA, entry.Resolutions = r.SHA, r.Resolutions
		}
	}

	dest := filepath.Join(metadata.RootPath, ReadSpacesDir, ReposDir, owner, repo)
	if err := exportReadspaceRepo(&entry, dest, metadata.Iteration, ReadspaceEventAdd, false); err != nil {
		return nil, err
	}

	if at >= 0 {
		metadata.ReadspaceRepos[at] = entry
	} else {
		metadata.ReadspaceRepos = append(metadata.ReadspaceRepos, entry)
	}
	if err := m.writeMetadata(metadata.RootPath, metadata); err != nil {
		return nil, err
	}
	return &entry, nil
}

// refreshReadspaceRepos fetches and re-exports every read-only repo of a
// slopspace into readSpaces/repos, recording what each resolved to in
// metadata (which the caller writes). iteration is the deployment the commit
// is for. A repo whose fetch fails is exported from what its clone already
// has, with the failure kept in its Error; only a repo that can't be exported
// at all is returned as an error.
func refreshReadspaceRepos(metadata *types.SlopspaceMetadata, readSpaces string, iteration int, event string) error {
	var errs []error
	for i := range metadata.ReadspaceRepos {
		r := &metadata.ReadspaceRepos[i]
		owner, name, _ := strings.Cut(r.Repo, "/")
		dest := filepath.Join(readSpaces, ReposDir, owner, name)
		if err := exportReadspaceRepo(r, dest, iteration, event, true); err != nil {
			errs = append(errs, fmt.Errorf("read-only repo %s: %w", r.Repo, err))
		}
	}
	return errors.Join(errs...)
}

// exportReadspaceRepo resolves r's ref in its clone (fetching first when
// fetch is set) and writes that commit's tree to dest, read-only, replacing
// whatever was there. A new commit is appended to r's Resolutions.
func exportReadspaceRepo(r *types.ReadspaceRepo, dest string, iteration int, event string, fetch bool) error {
	r.Error = ""
	if fetch {
		if err := fetchReadspaceSource(r.Source, r.Repo); err != nil {
			r.Error = err.Error()
		}
	}
	fail := func(err error) error {
		if r.Error != "" {
			err = fmt.Errorf("%w (after %s)", err, r.Error)
		}
		r.Error = err.Error()
		return err
	}
	sha, err := resolveReadspaceRef(r.Source, r.Ref)
	if err != nil {
		return fail(err)
	}
	if err := exportCommit(r.Source, sha, dest); err != nil {
		removeTree(dest)
		return fail(fmt.Errorf("export %s: %w", sha, err))
	}

	now := time.Now()
	if sha != r.SHA {
		r.Resolutions = append(r.Resolutions, types.ReadspaceResolution{SHA: sha, ResolvedAt: now, Iteration: iteration, Event: event})
		if n := len(r.Resolutions); n > types.MaxReadspaceResolutions {
			r.Resolutions = r.Resolutions[n-types.MaxReadspaceResolutions:]
		}
	}
	r.SHA, r.ResolvedAt = sha, now
	return nil
}

// fetchReadspaceSource brings a read-only repo's clone up to date with its
// origin: every branch and tag, so any ref it may follow resolves.
func fetchReadspaceSource(src, ownerRepo string) error {
	if err := gitauth.ScrubRemote(src, ownerRepo); err != nil {
		return err
	}
	out, err := gitauth.Command(src, "fetch", "--quiet", "--prune", "--tags", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveReadspaceRef is the commit ref names in the clone at src: origin's
// branch of that name first (the clone's own branches go stale), then a tag,
// then anything git can resolve (a commit). "" is origin's default branch.
func resolveReadspaceRef(src, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n") {
		return "", fmt.Errorf("ref %q isn't a branch, tag or commit name", ref)
	}
	candidates := []string{"refs/remotes/origin/HEAD", "HEAD"}
	if ref != "" {
		candidates = []string{"refs/remotes/origin/" + ref, "refs/tags/" + ref, ref}
	}
	for _, c := range candidates {
		out, err := exec.Command("git", "-C", src, "rev-parse", "--verify", "--quiet", c+"^{commit}").Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	if ref == "" {
		return "", fmt.Errorf("no default branch to follow in %s", src)
	}
	return "", fmt.Errorf("ref %q doesn't name a branch, tag or commit in %s", ref, src)
}

// exportCommit writes sha's tree from the repo at src into dest (replacing
// it) with git archive, then makes it read-only.
func exportCommit(src, sha, dest string) error {
	if err := removeTree(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	cmd := exec.Command("git", "-C", src, "archive", "--format=tar", sha)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(out, dest)
	if extractErr != nil {
		io.Copy(io.Discard, out) // let git finish writing
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if extractErr != nil {
		return extractErr
	}
	return makeReadOnly(dest)
}

// extractTar writes a git archive's entries under dest.
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0777|0600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, path); err != nil {
				return err
			}
		}
		// Anything else (git's pax global header, the commit id) isn't a file.
	}
}

// makeReadOnly takes the write bits off everything under root.
func makeReadOnly(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()&^0222)
	})
	if err != nil {
		return err
	}
	// Deepest first, so each directory is still writable while its children change.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0555); err != nil {
			return err
		}
	}
	return nil
}

// removeTree removes path like os.RemoveAll, first giving back the write
// permission makeReadOnly took from its directories (without it their
// entries can't be removed). A missing path is no error.
func removeTree(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0755)
		}
		return nil
	})
	return os.RemoveAll(path)
}
