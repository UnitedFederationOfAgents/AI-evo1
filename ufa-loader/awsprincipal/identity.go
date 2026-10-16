package awsprincipal

import (
	"context"
	"errors"
)

// CallerIdentity asks STS GetCallerIdentity who creds authenticate as,
// returning that principal's ARN -- e.g. local-representative's system-tab
// connectivity check (local-representative/connectivity.go), which needs
// both "do these keys work" and a principal name to show.
func CallerIdentity(ctx context.Context, creds Credentials) (string, error) {
	return callerIdentity(ctx, newClient(creds))
}

func callerIdentity(ctx context.Context, c *client) (string, error) {
	var out struct {
		Arn string `xml:"GetCallerIdentityResult>Arn"`
	}
	if err := c.sts(ctx, "GetCallerIdentity", nil, &out); err != nil {
		return "", err
	}
	if out.Arn == "" {
		return "", errors.New("GetCallerIdentity: response carried no Arn")
	}
	return out.Arn, nil
}
