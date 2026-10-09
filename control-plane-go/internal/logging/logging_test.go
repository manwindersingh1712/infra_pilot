package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	New("json", &buf).Info("hello", "deploymentId", "d1")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil || m["msg"] != "hello" || m["deploymentId"] != "d1" {
		t.Fatalf("got %q err %v", buf.String(), err)
	}
}

func TestTextFormatIsDefault(t *testing.T) {
	var buf bytes.Buffer
	New("anything-else", &buf).Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Fatalf("got %q", buf.String())
	}
}
