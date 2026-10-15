// Package awsprincipal gives each ufa-loader launch its own temporary IAM
// user, so the sub-application it wraps runs with credentials that are
// tagged with the host, date and app they were issued for, rather than with
// whatever long-lived credentials the host started ufa-loader with. See
// condocs/initialRobotImpls/Step5SubstepGPrompt.md Revision D and
// ufa-loader/README.md "Temporary AWS principals".
//
// It deliberately uses only the standard library (its own SigV4 signer and
// a small IAM/STS query-protocol client) rather than aws-sdk-go-v2: every
// module that imports ufa-loader/restartsignal pulls ufa-loader's
// requirements into its own module graph, and the SDK would drag a pinned
// set of AWS module versions into all of them.
package awsprincipal

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Credentials is one set of AWS access keys. SessionToken is empty for
// long-lived IAM user keys, including the ones Provision issues.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c Credentials) ok() bool { return c.AccessKeyID != "" && c.SecretAccessKey != "" }

// credentialEnvVars are the environment variables through which an AWS SDK
// or the AWS CLI might find credentials (or a source of them). ApplyToEnv
// clears all of them so the sub-application can only see the new keys.
// AWS_REGION / AWS_DEFAULT_REGION and the shared-file locations are left
// alone: they don't carry credentials by themselves.
var credentialEnvVars = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_SECURITY_TOKEN",
	"AWS_CREDENTIAL_EXPIRATION",
	// aws-sdk-go-v2 prefers a named profile over AWS_ACCESS_KEY_ID when
	// both are set, so a leftover profile would hide the new keys.
	"AWS_PROFILE",
	"AWS_DEFAULT_PROFILE",
	"AWS_ROLE_ARN",
	"AWS_ROLE_SESSION_NAME",
	"AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
}

// ResolveCredentials finds the static AWS credentials ufa-loader was started
// with, in this order: AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY(/
// AWS_SESSION_TOKEN) in the environment; then the selected profile
// (AWS_PROFILE, else AWS_DEFAULT_PROFILE, else "default") in the shared
// credentials file; then that profile in the shared config file. Profiles
// that only point somewhere else (SSO, role_arn, credential_process) and
// instance/container roles aren't supported and count as no credentials.
func ResolveCredentials() (Credentials, bool) {
	env := Credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	if env.ok() {
		return env, true
	}

	profile := selectedProfile()
	home, _ := os.UserHomeDir()
	if c := profileCredentials(sharedFile("AWS_SHARED_CREDENTIALS_FILE", home, "credentials"), profile); c.ok() {
		return c, true
	}
	cfg := sharedFile("AWS_CONFIG_FILE", home, "config")
	if c := profileCredentials(cfg, "profile "+profile); c.ok() {
		return c, true
	}
	if profile == "default" {
		if c := profileCredentials(cfg, "default"); c.ok() {
			return c, true
		}
	}
	return Credentials{}, false
}

// ApplyToEnv replaces every credential-bearing AWS variable in this
// process's environment with c, so anything launched afterwards (the
// sub-application) inherits c and nothing else.
//
// Clearing AWS_PROFILE also drops everything else the sub-application's SDK
// would have read from that profile -- most importantly its region, without
// which the SDK has no endpoint to call (the-conversationalist reports "no
// AWS region configured"). So when the environment names no region itself,
// the selected profile's region is looked up first and carried over as
// AWS_REGION.
func ApplyToEnv(c Credentials) {
	region := ""
	if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
		region = profileRegion()
	}
	for _, k := range credentialEnvVars {
		os.Unsetenv(k)
	}
	os.Setenv("AWS_ACCESS_KEY_ID", c.AccessKeyID)
	os.Setenv("AWS_SECRET_ACCESS_KEY", c.SecretAccessKey)
	if c.SessionToken != "" {
		os.Setenv("AWS_SESSION_TOKEN", c.SessionToken)
	}
	if region != "" {
		os.Setenv("AWS_REGION", region)
	}
}

// selectedProfile is the shared-config profile an AWS SDK would use:
// AWS_PROFILE, else AWS_DEFAULT_PROFILE, else "default".
func selectedProfile() string {
	return firstNonEmpty(os.Getenv("AWS_PROFILE"), os.Getenv("AWS_DEFAULT_PROFILE"), "default")
}

// profileRegion returns the region the selected profile sets in the shared
// config file, or "" if it sets none.
func profileRegion() string {
	profile := selectedProfile()
	home, _ := os.UserHomeDir()
	cfg := sharedFile("AWS_CONFIG_FILE", home, "config")
	if r := profileSection(cfg, "profile "+profile)["region"]; r != "" {
		return r
	}
	if profile == "default" {
		return profileSection(cfg, "default")["region"]
	}
	return ""
}

// sharedFile returns the path named by envVar, else ~/.aws/<name> (or ""
// when there's no home directory to look in).
func sharedFile(envVar, home, name string) string {
	if p := os.Getenv(envVar); p != "" {
		return p
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aws", name)
}

// profileCredentials reads aws_access_key_id / aws_secret_access_key /
// aws_session_token from section [section] of the INI file at path.
func profileCredentials(path, section string) Credentials {
	kv := profileSection(path, section)
	return Credentials{
		AccessKeyID:     kv["aws_access_key_id"],
		SecretAccessKey: kv["aws_secret_access_key"],
		SessionToken:    kv["aws_session_token"],
	}
}

// profileSection returns the key = value pairs (keys lower-cased) in section
// [section] of the INI file at path; nil if the file can't be read.
func profileSection(path, section string) map[string]string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	kv := map[string]string{}
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			in = strings.Join(strings.Fields(line[1:len(line)-1]), " ") == section
			continue
		}
		if !in {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		kv[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return kv
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
