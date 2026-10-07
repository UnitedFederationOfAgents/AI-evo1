package main

import (
	"errors"
	"image"
	"strings"
	"testing"
)

func TestCaptureForLRSavesScreenshot(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return litImage(8, 6), nil }
	orig := uploadToFiles
	t.Cleanup(func() { uploadToFiles = orig })
	var gotBase, gotName string
	var gotLen int
	uploadToFiles = func(base, filename string, content []byte) (*uploadedFile, error) {
		gotBase, gotName, gotLen = base, filename, len(content)
		return &uploadedFile{ID: "abcd_" + filename, Name: filename}, nil
	}

	res := captureForLR(RobotCaptureRequest{Req: "c1", Name: "Node A"}, func() (string, error) { return "http://lr", nil })
	if !res.Success || res.Req != "c1" || res.Width != 8 || res.Height != 6 {
		t.Fatalf("result = %+v", res)
	}
	if gotBase != "http://lr" || gotLen == 0 {
		t.Errorf("uploaded to %q (%d bytes)", gotBase, gotLen)
	}
	if !strings.HasPrefix(gotName, "ianar-capture-native-node-a-") || !strings.HasSuffix(gotName, ".png") {
		t.Errorf("name = %q", gotName)
	}
	if res.SavedAs != gotName || res.SavedID != "abcd_"+gotName {
		t.Errorf("saved as %q (%q)", res.SavedAs, res.SavedID)
	}
}

func TestCaptureForLRWithoutLR(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return litImage(4, 4), nil }
	res := captureForLR(RobotCaptureRequest{Req: "c2"}, func() (string, error) { return "", errors.New("not connected to local-representative") })
	if res.Success || !strings.Contains(res.Error, "not connected") {
		t.Errorf("result = %+v", res)
	}
}

func TestCaptureForLRCaptureFails(t *testing.T) {
	stubCapturePaths(t)
	res := captureForLR(RobotCaptureRequest{Req: "c3"}, func() (string, error) { return "http://lr", nil })
	if res.Success || res.Error == "" {
		t.Errorf("result = %+v", res)
	}
}
