package ufaconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestLoadMergePrecedence verifies the per-application file wins over
// global.yaml on a per-key basis, while keys only in global.yaml survive.
func TestLoadMergePrecedence(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, GlobalFile), "# shared\nlr-host: globalhost\nlr-port: 7000\nshared-only: keep\n")
	write(t, filepath.Join(dir, AppFile("federation-command")), "lr-port: 7100\nauto-connect: true\n")

	c, err := Load("federation-command", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := c.String("lr-host", "def"); got != "globalhost" {
		t.Errorf("lr-host = %q, want globalhost (from global.yaml)", got)
	}
	if got, _ := c.Int("lr-port", 0); got != 7100 {
		t.Errorf("lr-port = %d, want 7100 (app file wins)", got)
	}
	if got := c.String("shared-only", "def"); got != "keep" {
		t.Errorf("shared-only = %q, want keep", got)
	}
	if got, _ := c.Bool("auto-connect", false); !got {
		t.Errorf("auto-connect = false, want true")
	}
	if got := c.String("absent", "fallback"); got != "fallback" {
		t.Errorf("absent key = %q, want fallback", got)
	}
	if src := c.Source("lr-port"); src != filepath.Join(dir, AppFile("federation-command")) {
		t.Errorf("Source(lr-port) = %q, want the app file", src)
	}
}

// TestLoadMissingFilesOK verifies an empty config dir is not an error.
func TestLoadMissingFilesOK(t *testing.T) {
	c, err := Load("local-representative", t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.String("port", "8081"); got != "8081" {
		t.Errorf("expected default when no files present, got %q", got)
	}
	if len(c.Keys()) != 0 {
		t.Errorf("expected no keys, got %v", c.Keys())
	}
}

// TestNilConfigSafe verifies a nil *Config behaves as an empty config.
func TestNilConfigSafe(t *testing.T) {
	var c *Config
	if got := c.String("x", "d"); got != "d" {
		t.Errorf("nil String = %q, want d", got)
	}
	if got, err := c.Bool("x", true); err != nil || !got {
		t.Errorf("nil Bool = %v, %v; want true, nil", got, err)
	}
	if got, err := c.Int("x", 5); err != nil || got != 5 {
		t.Errorf("nil Int = %v, %v; want 5, nil", got, err)
	}
}

func TestParseScalarForms(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, GlobalFile), `plain: value
quoted: "spaced value"  # trailing comment
single: 'literal'
empty: ""
with-hash: "#ffffff"
trailing: bare # comment here
`)
	c, err := Load("x", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := map[string]string{
		"plain":     "value",
		"quoted":    "spaced value",
		"single":    "literal",
		"empty":     "",
		"with-hash": "#ffffff",
		"trailing":  "bare",
	}
	for k, want := range cases {
		if got := c.String(k, "<none>"); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadRejectsMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"no colon":      "just a line\n",
		"nested":        "parent:\n  child: v\n",
		"sequence":      "items: [a, b]\n",
		"bad key":       "bad key!: v\n",
		"unterminated":  `k: "oops` + "\n",
		"duplicate key": "k: 1\nk: 2\n",
		"empty value":   "k:\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, AppFile("x")), body)
			if _, err := Load("x", dir); err == nil {
				t.Fatalf("expected error for %q", body)
			}
		})
	}
}

func TestBoolAndIntErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, AppFile("x")), "flag: banana\nnum: twelve\n")
	c, err := Load("x", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := c.Bool("flag", false); err == nil {
		t.Errorf("expected error for non-boolean value")
	}
	if _, err := c.Int("num", 0); err == nil {
		t.Errorf("expected error for non-integer value")
	}
}

func TestExtractConfigDir(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDir  string
		wantRest []string
		wantErr  bool
	}{
		{"none", []string{"--auto-connect"}, "", []string{"--auto-connect"}, false},
		{"space form", []string{"--config", "/etc/ufa", "--dev"}, "/etc/ufa", []string{"--dev"}, false},
		{"equals form", []string{"--config=/etc/ufa", "--dev"}, "/etc/ufa", []string{"--dev"}, false},
		{"single dash", []string{"-config", "/tmp/c"}, "/tmp/c", []string{}, false},
		{"missing value", []string{"--config"}, "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, rest, err := ExtractConfigDir(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dir != tt.wantDir {
				t.Errorf("dir = %q, want %q", dir, tt.wantDir)
			}
			if len(rest) != len(tt.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tt.wantRest)
			}
			for i := range rest {
				if rest[i] != tt.wantRest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, rest[i], tt.wantRest[i])
				}
			}
		})
	}
}

// TestDefaultDirHonoursEnv verifies $UFA_CONFIG_DIR overrides the ~/.ufa/config
// default.
func TestDefaultDirHonoursEnv(t *testing.T) {
	t.Setenv("UFA_CONFIG_DIR", "/custom/ufa")
	if got := DefaultDir(); got != "/custom/ufa" {
		t.Errorf("DefaultDir() = %q, want /custom/ufa", got)
	}
}
