package awsprincipal

import (
	"context"
	"testing"
	"time"
)

func TestCallerIdentityReturnsArn(t *testing.T) {
	f := newFakeIAM()
	f.addUser("someone", nil, time.Now(), time.Time{})
	c := testClient(t, f, time.Now())
	c = c.withCreds(Credentials{AccessKeyID: "AKIASEED1", SecretAccessKey: "x"})

	arn, err := callerIdentity(context.Background(), c)
	if err != nil {
		t.Fatalf("callerIdentity: %v", err)
	}
	if want := "arn:aws:iam::123456789012:user" + Path + "someone"; arn != want {
		t.Errorf("arn = %q, want %q", arn, want)
	}
}

func TestCallerIdentityRejectedKey(t *testing.T) {
	f := newFakeIAM()
	c := testClient(t, f, time.Now())

	if _, err := callerIdentity(context.Background(), c); err == nil {
		t.Fatal("callerIdentity with an unknown key succeeded")
	}
}
