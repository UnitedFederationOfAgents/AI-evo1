package awsprincipal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Path is the IAM path every temporary principal is created under; the
	// cleanup pass only ever looks at users under it.
	Path = "/ufa-temp/"

	namePrefix = "ufa-tmp-"
	dateLayout = "2006-01-02"

	tagHost = "ufa-host"
	tagDate = "date"
	tagApp  = "app"

	// policyARN is the access the temporary principal is granted. Revision
	// D starts high and scales back later.
	policyARN = "arn:aws:iam::aws:policy/AdministratorAccess"

	// New IAM access keys take a few seconds to be accepted everywhere;
	// Provision waits up to confirmTimeout for STS to accept the new key so
	// the sub-application doesn't start with keys AWS still rejects.
	confirmTimeout  = 30 * time.Second
	confirmInterval = 2 * time.Second
)

// ErrNoCredentials is Provision's error when ufa-loader was started without
// any AWS credentials it can use. Callers are expected to carry on silently.
var ErrNoCredentials = errors.New("no AWS credentials available")

// Options configures Provision.
type Options struct {
	HostID string // ufa host id; goes in the user name and the ufa-host tag
	App    string // the loaded sub-application; goes in the app tag
	// IdleDays is how long a temporary principal from a previous day may go
	// unused before the cleanup pass deletes it.
	IdleDays int
}

// Result describes what Provision did.
type Result struct {
	UserName    string
	Credentials Credentials // the new principal's keys; never persisted
	Confirmed   bool        // STS accepted the new keys before Provision returned
	Removed     int         // stale temporary principals deleted
	CleanupErrs int         // stale temporary principals that couldn't be deleted
}

// Provision creates a temporary IAM user for this launch using the
// credentials ufa-loader was started with (see ResolveCredentials), tagged
// ufa-host/date/app, with an access key; then deletes temporary users from
// previous days that have gone unused for opts.IdleDays. It returns
// ErrNoCredentials when there are no starting credentials. It does not touch
// the environment -- see ApplyToEnv.
func Provision(ctx context.Context, opts Options) (*Result, error) {
	creds, ok := ResolveCredentials()
	if !ok {
		return nil, ErrNoCredentials
	}
	return provision(ctx, newClient(creds), opts)
}

func provision(ctx context.Context, c *client, opts Options) (*Result, error) {
	now := c.now().UTC()
	name := userName(opts.HostID, now)

	tags := [][2]string{
		{tagHost, tagValue(opts.HostID)},
		{tagDate, now.Format(dateLayout)},
		{tagApp, tagValue(opts.App)},
	}
	params := url.Values{"UserName": {name}, "Path": {Path}}
	for i, t := range tags {
		n := strconv.Itoa(i + 1)
		params.Set("Tags.member."+n+".Key", t[0])
		params.Set("Tags.member."+n+".Value", t[1])
	}
	if err := c.iam(ctx, "CreateUser", params, nil); err != nil {
		return nil, err
	}

	var key struct {
		AccessKeyID     string `xml:"CreateAccessKeyResult>AccessKey>AccessKeyId"`
		SecretAccessKey string `xml:"CreateAccessKeyResult>AccessKey>SecretAccessKey"`
	}
	err := c.iam(ctx, "AttachUserPolicy", url.Values{"UserName": {name}, "PolicyArn": {policyARN}}, nil)
	if err == nil {
		err = c.iam(ctx, "CreateAccessKey", url.Values{"UserName": {name}}, &key)
	}
	if err == nil && (key.AccessKeyID == "" || key.SecretAccessKey == "") {
		err = errors.New("CreateAccessKey: response carried no key")
	}
	if err != nil {
		// Don't leave a half-built principal behind.
		if derr := deleteUser(ctx, c, name); derr != nil {
			return nil, fmt.Errorf("%w (and removing %s again failed: %v)", err, name, derr)
		}
		return nil, err
	}

	res := &Result{
		UserName:    name,
		Credentials: Credentials{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey},
	}
	res.Confirmed = confirm(ctx, c.withCreds(res.Credentials))
	res.Removed, res.CleanupErrs = cleanup(ctx, c, now, opts.IdleDays)
	return res, nil
}

// confirm polls STS GetCallerIdentity with the new keys until it succeeds
// or confirmTimeout passes.
func confirm(ctx context.Context, c *client) bool {
	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	for {
		if c.sts(ctx, "GetCallerIdentity", nil, nil) == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(confirmInterval):
		}
	}
}

// cleanup deletes users under Path whose date tag is before today (UTC) and
// that haven't been used for idleDays: "used" is the most recent access-key
// use IAM reports, or the user's creation if a key was never used. Users
// missing the ufa-host or date tag aren't ours and are left alone. It
// returns how many were removed and how many couldn't be.
func cleanup(ctx context.Context, c *client, now time.Time, idleDays int) (removed, failed int) {
	today := now.Format(dateLayout)
	cutoff := now.Add(-time.Duration(idleDays) * 24 * time.Hour)

	users, err := listUsers(ctx, c)
	if err != nil {
		return 0, 1
	}
	for _, u := range users {
		stale, err := isStale(ctx, c, u, today, cutoff)
		if err != nil {
			failed++
			continue
		}
		if !stale {
			continue
		}
		if err := deleteUser(ctx, c, u.UserName); err != nil {
			failed++
			continue
		}
		removed++
	}
	return removed, failed
}

type iamUser struct {
	UserName   string    `xml:"UserName"`
	CreateDate time.Time `xml:"CreateDate"`
}

func listUsers(ctx context.Context, c *client) ([]iamUser, error) {
	var all []iamUser
	marker := ""
	for {
		params := url.Values{"PathPrefix": {Path}}
		if marker != "" {
			params.Set("Marker", marker)
		}
		var page struct {
			Users       []iamUser `xml:"ListUsersResult>Users>member"`
			IsTruncated bool      `xml:"ListUsersResult>IsTruncated"`
			Marker      string    `xml:"ListUsersResult>Marker"`
		}
		if err := c.iam(ctx, "ListUsers", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Users...)
		if !page.IsTruncated || page.Marker == "" {
			return all, nil
		}
		marker = page.Marker
	}
}

func isStale(ctx context.Context, c *client, u iamUser, today string, cutoff time.Time) (bool, error) {
	var tags struct {
		Tags []struct {
			Key   string `xml:"Key"`
			Value string `xml:"Value"`
		} `xml:"ListUserTagsResult>Tags>member"`
	}
	if err := c.iam(ctx, "ListUserTags", url.Values{"UserName": {u.UserName}}, &tags); err != nil {
		return false, err
	}
	var host, date string
	for _, t := range tags.Tags {
		switch t.Key {
		case tagHost:
			host = t.Value
		case tagDate:
			date = t.Value
		}
	}
	if host == "" {
		return false, nil
	}
	if _, err := time.Parse(dateLayout, date); err != nil || date >= today {
		return false, nil
	}

	lastUsed := u.CreateDate
	keys, err := accessKeys(ctx, c, u.UserName)
	if err != nil {
		return false, err
	}
	for _, id := range keys {
		var lu struct {
			LastUsedDate time.Time `xml:"GetAccessKeyLastUsedResult>AccessKeyLastUsed>LastUsedDate"`
		}
		if err := c.iam(ctx, "GetAccessKeyLastUsed", url.Values{"AccessKeyId": {id}}, &lu); err != nil {
			return false, err
		}
		if lu.LastUsedDate.After(lastUsed) {
			lastUsed = lu.LastUsedDate
		}
	}
	return lastUsed.Before(cutoff), nil
}

func accessKeys(ctx context.Context, c *client, user string) ([]string, error) {
	var out struct {
		IDs []string `xml:"ListAccessKeysResult>AccessKeyMetadata>member>AccessKeyId"`
	}
	if err := c.iam(ctx, "ListAccessKeys", url.Values{"UserName": {user}}, &out); err != nil {
		return nil, err
	}
	return out.IDs, nil
}

// deleteUser removes a temporary user and what Provision attached to it
// (access keys, managed policies) -- IAM refuses DeleteUser while either
// remains.
func deleteUser(ctx context.Context, c *client, user string) error {
	keys, err := accessKeys(ctx, c, user)
	if err != nil {
		return err
	}
	for _, id := range keys {
		if err := c.iam(ctx, "DeleteAccessKey", url.Values{"UserName": {user}, "AccessKeyId": {id}}, nil); err != nil {
			return err
		}
	}
	var pols struct {
		ARNs []string `xml:"ListAttachedUserPoliciesResult>AttachedPolicies>member>PolicyArn"`
	}
	if err := c.iam(ctx, "ListAttachedUserPolicies", url.Values{"UserName": {user}}, &pols); err != nil {
		return err
	}
	for _, arn := range pols.ARNs {
		if err := c.iam(ctx, "DetachUserPolicy", url.Values{"UserName": {user}, "PolicyArn": {arn}}, nil); err != nil {
			return err
		}
	}
	return c.iam(ctx, "DeleteUser", url.Values{"UserName": {user}}, nil)
}

// userName is ufa-tmp-<host>-<YYYYMMDD>-<6 random>, kept within IAM's
// 64-character limit and character set. The random suffix keeps several
// loaders on one host on one day (e.g. local-representative and the
// conversationalist) from colliding.
func userName(hostID string, now time.Time) string {
	host := sanitize(hostID, "+=,.@_-")
	const fixed = len(namePrefix) + len("-20060102-") + 6
	if len(host) > 64-fixed {
		host = host[:64-fixed]
	}
	if host == "" {
		host = "unknown"
	}
	return namePrefix + host + "-" + now.Format("20060102") + "-" + randomSuffix(6)
}

// tagValue keeps v within IAM's tag-value character set and length.
func tagValue(v string) string {
	v = sanitize(v, " _.:/=+-@")
	if len(v) > 256 {
		v = v[:256]
	}
	return v
}

// sanitize replaces every byte that isn't an ASCII letter, digit or one of
// allowed with '-'.
func sanitize(s, allowed string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r < 128 && strings.ContainsRune(allowed, r):
			return r
		}
		return '-'
	}, s)
}

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}
