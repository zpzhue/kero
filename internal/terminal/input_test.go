package terminal

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPlainInputMultilineAndEOF(t *testing.T) {
	in := &Input{reader: bufio.NewReader(strings.NewReader("first\\\r\nsecond\r\nlast"))}
	for _, want := range []string{"first\nsecond", "last"} {
		got, err := in.Read()
		if err != nil || got != want {
			t.Fatalf("Read() = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := in.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}
func TestPlainInputDiscardsUnfinishedContinuation(t *testing.T) {
	in := &Input{reader: bufio.NewReader(strings.NewReader("unfinished\\\n"))}
	if got, err := in.Read(); got != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = %q, %v; expected empty EOF", got, err)
	}
}
