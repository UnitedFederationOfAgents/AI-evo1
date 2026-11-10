// Package ufaconfig provides shared config-file loading for UFA sub-applications
// (federation-command, local-representative, ...).
//
// Each sub-application reads two YAML files from a config directory:
//
//	<dir>/global.yaml            shared defaults for every sub-application
//	<dir>/<app>.yaml             per-application overrides
//
// <dir> defaults to ~/.ufa/config (or $UFA_CONFIG_DIR) and can be overridden at
// launch with the --config flag, parsed by ExtractConfigDir.
//
// Resolution order, highest priority first:
//
//  1. command-line flags        (applied by the caller after Load)
//  2. <dir>/<app>.yaml          (per-application config)
//  3. <dir>/global.yaml         (shared config; app file wins per key)
//  4. the caller's built-in default (the def argument to Bool/Int/String)
//
// The YAML accepted here is deliberately a small subset: a flat mapping of
// "key: value" scalar pairs. Blank lines and "#" comments are ignored. Nested
// mappings, sequences and anchors are rejected so typos surface as errors
// instead of being silently dropped.
package ufaconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GlobalFile is the shared config filename scanned in the config directory.
const GlobalFile = "global.yaml"

// ConfigFlag is the launch argument that overrides the config directory.
const ConfigFlag = "--config"

// AppFile returns the per-application config filename, e.g.
// "federation-command.yaml".
func AppFile(app string) string { return app + ".yaml" }

// DefaultDir is the directory scanned when --config is not supplied:
// $UFA_CONFIG_DIR if set, otherwise ~/.ufa/config.
func DefaultDir() string {
	if d := os.Getenv("UFA_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".ufa", "config")
	}
	return filepath.Join(home, ".ufa", "config")
}

// entry is one merged config value plus the file it came from.
type entry struct {
	raw    string
	source string
}

// Config is a merged, read-only view of the config files for one
// sub-application. A nil *Config is valid and behaves as an empty config, so
// callers may skip loading entirely.
type Config struct {
	app    string
	dir    string
	values map[string]entry
}

// Load reads <dir>/global.yaml then <dir>/<app>.yaml and merges them per key,
// with the per-application file taking precedence. A missing file is not an
// error; a malformed file is. If dir is "", DefaultDir() is used.
func Load(app, dir string) (*Config, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	c := &Config{app: app, dir: dir, values: make(map[string]entry)}
	for _, name := range []string{GlobalFile, AppFile(app)} {
		path := filepath.Join(dir, name)
		kv, err := parseFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for k, v := range kv {
			c.values[k] = entry{raw: v, source: path}
		}
	}
	return c, nil
}

func (c *Config) lookup(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	e, ok := c.values[key]
	return e.raw, ok
}

// String returns the configured value for key, or def when no config file
// supplied it.
func (c *Config) String(key, def string) string {
	if v, ok := c.lookup(key); ok {
		return v
	}
	return def
}

// Bool returns the configured boolean for key, or def when no config file
// supplied it. Accepted spellings: true/false, yes/no, on/off, 1/0
// (case-insensitive). A present-but-unparseable value is an error.
func (c *Config) Bool(key string, def bool) (bool, error) {
	v, ok := c.lookup(key)
	if !ok {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return def, fmt.Errorf("ufaconfig: %s: key %q: %q is not a boolean", c.Source(key), key, v)
}

// Int returns the configured integer for key, or def when no config file
// supplied it. A present-but-unparseable value is an error.
func (c *Config) Int(key string, def int) (int, error) {
	v, ok := c.lookup(key)
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def, fmt.Errorf("ufaconfig: %s: key %q: %q is not an integer", c.Source(key), key, v)
	}
	return n, nil
}

// Source reports the config file a key was read from, or "(default)" if no file
// supplied it.
func (c *Config) Source(key string) string {
	if c != nil {
		if e, ok := c.values[key]; ok {
			return e.source
		}
	}
	return "(default)"
}

// Keys lists every key supplied by the config files, sorted.
func (c *Config) Keys() []string {
	if c == nil {
		return nil
	}
	ks := make([]string, 0, len(c.values))
	for k := range c.values {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Dir reports the directory the config was loaded from.
func (c *Config) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// ExtractConfigDir scans args for a config-directory override (--config <dir>,
// --config=<dir>, or the single-dash forms) and returns the override ("" if
// none), the remaining args with the flag removed, and any error.
func ExtractConfigDir(args []string) (dir string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-config":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires a directory path", arg)
			}
			dir = args[i+1]
			i++
		case strings.HasPrefix(arg, "--config="):
			dir = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-config="):
			dir = strings.TrimPrefix(arg, "-config=")
		default:
			rest = append(rest, arg)
		}
	}
	return dir, rest, nil
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// parseFile reads the flat "key: value" YAML subset described in the package
// doc. A missing file is returned as an os.ErrNotExist-wrapping error.
func parseFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line != strings.TrimLeft(line, " \t") {
			return nil, fmt.Errorf(`%s:%d: indented lines are not supported (flat "key: value" only)`, path, n+1)
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon < 0 {
			return nil, fmt.Errorf(`%s:%d: expected "key: value", got %q`, path, n+1, trimmed)
		}
		key := strings.TrimSpace(trimmed[:colon])
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%s:%d: invalid config key %q", path, n+1, key)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate config key %q", path, n+1, key)
		}
		val, verr := parseScalar(strings.TrimSpace(trimmed[colon+1:]))
		if verr != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, n+1, verr)
		}
		out[key] = val
	}
	return out, nil
}

// parseScalar interprets the value half of a "key: value" line.
func parseScalar(s string) (string, error) {
	if s == "" {
		return "", errors.New(`missing value (use "" for an empty string)`)
	}
	switch s[0] {
	case '"', '\'':
		quote := s[0]
		for i := 1; i < len(s); i++ {
			if quote == '"' && s[i] == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if s[i] == quote {
				if rest := strings.TrimSpace(s[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
					return "", fmt.Errorf("unexpected text after quoted value: %q", rest)
				}
				inner := s[1:i]
				if quote == '"' {
					inner = strings.ReplaceAll(inner, `\"`, `"`)
					inner = strings.ReplaceAll(inner, `\\`, `\`)
				}
				return inner, nil
			}
		}
		return "", errors.New("unterminated quoted value")
	case '[', '{', '&', '*', '|', '>':
		return "", fmt.Errorf("unsupported YAML value %q (only flat scalars are supported)", s)
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s, nil
}
