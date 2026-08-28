package mcp

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestDecodeFilesText(t *testing.T) {
	got, err := DecodeFiles([]File{{Path: "index.html", Text: "<h1>hi</h1>"}})
	if err != nil {
		t.Fatalf("DecodeFiles: %v", err)
	}
	if len(got) != 1 || got[0].Path != "index.html" || string(got[0].Data) != "<h1>hi</h1>" {
		t.Fatalf("got %+v", got)
	}
}

func TestDecodeFilesBase64(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	got, err := DecodeFiles([]File{{Path: "logo.png", Base64: base64.StdEncoding.EncodeToString(raw)}})
	if err != nil {
		t.Fatalf("DecodeFiles: %v", err)
	}
	if string(got[0].Data) != string(raw) {
		t.Fatalf("round trip lost bytes: %v", got[0].Data)
	}
}

// An empty file is legal, but only when the caller says so explicitly. The
// distinction matters: "text is the empty string" is a request, "neither field
// is set" is a forgotten argument, and publishing an empty index.html for the
// second one is the worse outcome.
func TestDecodeFilesEmptyTextIsNotContent(t *testing.T) {
	if _, err := DecodeFiles([]File{{Path: "a.txt", Text: ""}}); err == nil {
		t.Fatal("a file with neither text nor base64 should be refused")
	}
}

func TestDecodeFilesRejections(t *testing.T) {
	cases := []struct {
		name  string
		file  File
		match string
	}{
		{"both set", File{Path: "a", Text: "x", Base64: "eA=="}, "not both"},
		{"neither set", File{Path: "a"}, "no content"},
		{"no path", File{Text: "x"}, "path is required"},
		{"bad base64", File{Path: "a", Base64: "not base64!!"}, "not valid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeFiles([]File{c.file})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.match) {
				t.Fatalf("message %q does not mention %q", err, c.match)
			}
		})
	}
}

// The cap is on the call, not on any single file: three files of 3 MiB are as
// much of a mistake as one file of 9 MiB.
func TestDecodeFilesCapIsCumulative(t *testing.T) {
	chunk := strings.Repeat("x", 3<<20)
	_, err := DecodeFiles([]File{
		{Path: "a", Text: chunk},
		{Path: "b", Text: chunk},
		{Path: "c", Text: chunk},
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	// The message has to route the agent somewhere it can succeed, or the
	// agent will simply retry the same call.
	for _, want := range []string{"write_files", "WebDAV", "zip"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %v", want, err)
		}
	}
}

func TestDecodeFilesUnderCapPasses(t *testing.T) {
	if _, err := DecodeFiles([]File{{Path: "a", Text: strings.Repeat("x", MaxContentBytes)}}); err != nil {
		t.Fatalf("exactly at the cap should pass: %v", err)
	}
}

func TestDecodeFilesEmptyList(t *testing.T) {
	got, err := DecodeFiles(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("an empty file list is legal (create_site allows it): %v %v", got, err)
	}
}
