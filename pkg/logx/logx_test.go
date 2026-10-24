package logx

import (
	"bytes"
	"strings"
	"testing"
)

func capture(t *testing.T, l Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldLevel := GetLevel()
	SetOutput(&buf)
	SetLevel(l)
	t.Cleanup(func() { SetLevel(oldLevel) })
	return &buf
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Level{"": Info, "INFO": Info, "off": Off, "debug": Debug, " trace ": Trace} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := Parse("verbose"); err == nil {
		t.Error("expected error for unknown level")
	}
}

func TestLevelsFilter(t *testing.T) {
	buf := capture(t, Info)
	Infof("hello %d", 1)
	Debugf("hidden")
	if !strings.Contains(buf.String(), "hello 1") || strings.Contains(buf.String(), "hidden") {
		t.Errorf("unexpected output: %q", buf.String())
	}

	buf = capture(t, Off)
	Infof("nothing")
	if buf.Len() != 0 {
		t.Errorf("off level printed %q", buf.String())
	}
}

func TestBlockTruncation(t *testing.T) {
	long := strings.Repeat("x", previewLimit+500)

	buf := capture(t, Debug)
	Block(Debug, "body", long)
	if !strings.Contains(buf.String(), "500 more bytes") {
		t.Errorf("debug level should truncate: %d bytes written", buf.Len())
	}

	buf = capture(t, Trace)
	Block(Debug, "body", long)
	if strings.Contains(buf.String(), "more bytes") || !strings.Contains(buf.String(), long) {
		t.Error("trace level should print the full body")
	}
}
