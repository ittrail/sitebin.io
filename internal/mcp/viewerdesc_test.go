package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// Agents pick a mode from this text. Since the File viewer opens on a file
// list, "a single document" would steer an agent with twenty PDFs to the web
// server, whose front page is then a 404.
func TestModeDescriptionSaysTheViewerListsFiles(t *testing.T) {
	f, _ := reflect.TypeOf(Settings{}).FieldByName("Mode")
	desc := f.Tag.Get("jsonschema")
	if strings.Contains(desc, "single document") {
		t.Errorf("mode description still says a single document: %q", desc)
	}
	if !strings.Contains(desc, "file list") {
		t.Errorf("mode description does not mention the file list: %q", desc)
	}
}
