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
	if got, want := listedFiles(out, false), []string{"a/y.go", "b/x.go", "condocs/Foo.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listedFiles = %v, want %v", got, want)
	}
}

func TestTurnPreamble(t *testing.T) {
	got := turnPreamble(
		[]string{"a/t1", "a/t2"},
		[]string{"a/t1@1111111aaa", "a/t2@2222222bbb", "a/condoc@3333333ccc", "a/pointer@4444444ddd"},
		map[string]string{"a/t1": "5555555eee", "a/t2": "2222222bbb"},
	)
	want := "- a/t1: [`1111111`](https://github.com/a/t1/commit/1111111aaa) → [`5555555`](https://github.com/a/t1/commit/5555555eee)\n" +
		"- a/t2: (no changes)\n" +
		"- a/condoc: [`3333333`](https://github.com/a/condoc/commit/3333333ccc) → this reply\n" +
		"- a/pointer: [`4444444`](https://github.com/a/pointer/commit/4444444ddd) → this reply"
	if got != want {
		t.Errorf("turnPreamble =\n%s\nwant\n%s", got, want)
	}
}
