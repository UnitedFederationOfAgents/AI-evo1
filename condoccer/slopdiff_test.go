package main

import (
	"reflect"
	"testing"
)

func TestParseRepoShas(t *testing.T) {
	shas, order, err := parseRepoShas("a/b@abc1234, c/d@def5678,")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b", "c/d"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if shas["a/b"] != "abc1234" || shas["c/d"] != "def5678" {
		t.Errorf("shas = %v", shas)
	}
	if shas, order, err := parseRepoShas(""); err != nil || len(shas) != 0 || len(order) != 0 {
		t.Errorf("empty list: %v %v %v", shas, order, err)
	}
	for _, bad := range []string{"a/b", "a/b@", "a/b@HEAD", "a/b@abc;rm"} {
		if _, _, err := parseRepoShas(bad); err == nil {
			t.Errorf("parseRepoShas(%q) accepted", bad)
		}
	}
}

func TestChangedFiles(t *testing.T) {
	out := []byte("\nb/x.go\na/y.go\n\nb/x.go\ncondocs/Foo.md\n")
	if got, want := changedFiles(out), []string{"a/y.go", "b/x.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changedFiles = %v, want %v", got, want)
	}
}
