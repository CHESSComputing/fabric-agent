// Package logx is a tiny leveled logger for tracing what the agent does:
// which node APIs are called, what the language model was asked and what it
// answered. Output goes to stderr so that results on stdout can be piped.
//
// Levels (each includes the previous ones):
//
//	off    nothing
//	info   one line per pipeline step, HTTP call and LLM call, with timings
//	debug  + response bodies, prompts and raw LLM replies (truncated)
//	trace  + the same, untruncated
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Level int

const (
	Off Level = iota
	Info
	Debug
	Trace
)

func (l Level) String() string {
	switch l {
	case Off:
		return "off"
	case Info:
		return "info"
	case Debug:
		return "debug"
	case Trace:
		return "trace"
	}
	return "?"
}

// previewLimit is how many bytes of a block are shown below trace level.
const previewLimit = 1500

var (
	mu    sync.Mutex
	level           = Info
	out   io.Writer = os.Stderr
)

// Parse converts a configuration string to a Level. The empty string is Info.
func Parse(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "quiet":
		return Off, nil
	case "", "info":
		return Info, nil
	case "debug":
		return Debug, nil
	case "trace":
		return Trace, nil
	}
	return Info, fmt.Errorf("unknown log level %q (use off, info, debug or trace)", s)
}

func SetLevel(l Level) {
	mu.Lock()
	level = l
	mu.Unlock()
}

func GetLevel() Level {
	mu.Lock()
	defer mu.Unlock()
	return level
}

func SetOutput(w io.Writer) {
	mu.Lock()
	out = w
	mu.Unlock()
}

// Enabled reports whether messages of level l are currently printed.
func Enabled(l Level) bool {
	mu.Lock()
	defer mu.Unlock()
	return l != Off && l <= level
}

func logf(l Level, format string, args ...interface{}) {
	if !Enabled(l) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(out, "%s [%-5s] %s\n", time.Now().Format("15:04:05.000"), l, msg)
}

func Infof(format string, args ...interface{})  { logf(Info, format, args...) }
func Debugf(format string, args ...interface{}) { logf(Debug, format, args...) }
func Tracef(format string, args ...interface{}) { logf(Trace, format, args...) }

// Block logs a title followed by an indented multi-line body. Below trace
// level, long bodies are cut to previewLimit bytes.
func Block(l Level, title, body string) {
	if !Enabled(l) {
		return
	}
	b := strings.TrimRight(body, "\n")
	note := ""
	if len(b) > previewLimit && !Enabled(Trace) {
		note = fmt.Sprintf("... [%d more bytes; set log_level: trace to see everything]", len(b)-previewLimit)
		b = b[:previewLimit]
		for i := 0; i < 3 && len(b) > 0 && !utf8.ValidString(b); i++ {
			b = b[:len(b)-1] // do not cut a multi-byte character in half
		}
	}
	var sb strings.Builder
	for _, line := range strings.Split(b, "\n") {
		sb.WriteString("\n    | ")
		sb.WriteString(line)
	}
	if note != "" {
		sb.WriteString("\n    | ")
		sb.WriteString(note)
	}
	logf(l, "%s%s", title, sb.String())
}
