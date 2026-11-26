package terminal

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func testRenderer() (*Renderer, *bytes.Buffer) {
	var buf bytes.Buffer
	return NewRenderer(&buf), &buf
}

func TestWriteReplyStreamsFragmentsOnce(t *testing.T) {
	r, buf := testRenderer()
	fragments := []string{"first fragment", "\n", "second ", "fragment"}
	full := strings.Join(fragments, "")

	r.StartReply()
	for _, fragment := range fragments {
		r.WriteReply(fragment)
	}
	if !r.EndReply() {
		t.Fatal("EndReply reported no text after nonempty fragments")
	}

	out := buf.String()
	if got := strings.Count(out, full); got != 1 {
		t.Fatalf("joined fragments appear %d times, want 1; output %q", got, out)
	}
	if !strings.Contains(out, "first fragment\nsecond fragment") {
		t.Fatalf("stream did not preserve newline placement; output %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("EndReply did not close the reply line; output %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("EndReply added a blank line; output %q", out)
	}
}

func TestWriteReplyEmptyTextWritesNothing(t *testing.T) {
	r, buf := testRenderer()
	r.StartReply()
	r.WriteReply("")
	if r.EndReply() {
		t.Fatal("EndReply reported text when only empty fragments were written")
	}
	if buf.Len() != 0 {
		t.Fatalf("empty reply wrote output %q", buf.String())
	}
}

func TestWriteReplyExistingTrailingNewlineIsNotDoubled(t *testing.T) {
	r, buf := testRenderer()
	r.StartReply()
	r.WriteReply("already closed\n")
	if !r.EndReply() {
		t.Fatal("EndReply reported no text")
	}
	out := buf.String()
	if !strings.HasSuffix(out, "already closed\n") {
		t.Fatalf("reply content or trailing newline changed; output %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("reply ended with a blank line; output %q", out)
	}
}

func TestWriteReplyNormalizesCRLFAcrossFragments(t *testing.T) {
	r, buf := testRenderer()
	r.StartReply()
	r.WriteReply("alpha\r")
	r.WriteReply("\nbeta\r")
	r.WriteReply("\ngamma")
	if !r.EndReply() {
		t.Fatal("EndReply reported no text")
	}

	out := buf.String()
	if strings.ContainsRune(out, '\r') {
		t.Fatalf("CRLF was not normalized; output %q", out)
	}
	if !strings.Contains(out, "alpha\nbeta\ngamma") {
		t.Fatalf("CRLF split across fragments broke line structure; output %q", out)
	}
}

func TestWriteReplyEscapesControlsAndKeepsWhitespace(t *testing.T) {
	r, buf := testRenderer()
	r.StartReply()
	r.WriteReply("before\x1b[31mred")
	r.WriteReply("\bafter\b")
	r.WriteReply("standalone\rcarriage")
	r.WriteReply("\tindent\nnext line")
	if !r.EndReply() {
		t.Fatal("EndReply reported no text")
	}

	out := buf.String()
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("raw ESC reached the output: %q", out)
	}
	if strings.ContainsRune(out, '\b') {
		t.Fatalf("raw backspace reached the output: %q", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Fatalf("raw standalone CR reached the output: %q", out)
	}
	for _, want := range []string{"before", "red", "after", "standalone", "carriage", "indent", "next line"} {
		if !strings.Contains(out, want) {
			t.Fatalf("visible text %q missing from output %q", want, out)
		}
	}
	if !strings.Contains(out, "\t") {
		t.Fatalf("tab was not preserved; output %q", out)
	}
	if !strings.Contains(out, "\n") {
		t.Fatalf("newline was not preserved; output %q", out)
	}
}

func TestWriteReplyEscapesStandaloneCRAcrossFragments(t *testing.T) {
	r, buf := testRenderer()
	r.StartReply()
	r.WriteReply("left\r")
	r.WriteReply("right")
	if !r.EndReply() {
		t.Fatal("EndReply reported no text")
	}

	out := buf.String()
	if strings.ContainsRune(out, '\r') {
		t.Fatalf("CR at a fragment boundary was not escaped; output %q", out)
	}
	left, right := strings.Index(out, "left"), strings.Index(out, "right")
	if left < 0 || right < 0 || left >= right {
		t.Fatalf("visible text around the CR was not preserved; output %q", out)
	}
}

func TestSafeLeavesVisibleTextUnchanged(t *testing.T) {
	const input = "plain text 123 你好\twith tab\nand newline"
	if got := Safe(input); got != input {
		t.Fatalf("Safe changed visible text: got %q, want %q", got, input)
	}
}

func TestSafeEscapesControlsAndNormalizesCRLF(t *testing.T) {
	const input = "esc\x1b[31mred\bback\rstandalone\ttab\nline"
	got := Safe(input)

	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("Safe left raw ESC: %q", got)
	}
	if strings.ContainsRune(got, '\b') {
		t.Fatalf("Safe left raw backspace: %q", got)
	}
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("Safe left raw CR: %q", got)
	}
	for _, want := range []string{"esc", "red", "back", "standalone", "tab", "line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Safe dropped visible text %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "\t") || !strings.Contains(got, "\n") {
		t.Fatalf("Safe dropped tab or newline: %q", got)
	}

	normalized := Safe("left\r\nright")
	if strings.ContainsRune(normalized, '\r') {
		t.Fatalf("Safe did not normalize CRLF: %q", normalized)
	}
	if !strings.Contains(normalized, "left\nright") {
		t.Fatalf("Safe lost the line break in CRLF: %q", normalized)
	}
}

func TestToolResultTruncatesAfterTwelveLines(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("%02d-preview-line", i+1)
	}

	r, buf := testRenderer()
	r.ToolResult(strings.Join(lines, "\n"))
	out := buf.String()

	for _, want := range lines[:12] {
		if !strings.Contains(out, want) {
			t.Fatalf("preview line %q missing from truncated output %q", want, out)
		}
	}
	for _, dropped := range lines[12:] {
		if strings.Contains(out, dropped) {
			t.Fatalf("output beyond the line limit %q was printed: %q", dropped, out)
		}
	}

	preview := strings.Join(lines[:12], "\n")
	if strings.TrimSpace(out) == strings.TrimSpace(preview) {
		t.Fatalf("truncated output has no notice beyond the preview: %q", out)
	}
}

func TestToolResultTruncatesLongUTF8WithoutBreakingIt(t *testing.T) {
	const tail = "TAILMARKER"
	body := strings.Repeat("界", 2500) + tail

	r, buf := testRenderer()
	r.ToolResult(body)
	out := buf.String()

	if !utf8.ValidString(out) {
		t.Fatalf("truncated output is not valid UTF-8: %q", out)
	}
	if strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("truncated output contains a replacement rune: %q", out)
	}
	if strings.Contains(out, tail) {
		t.Fatalf("output beyond the rune limit was printed: %q", out)
	}
	if !strings.Contains(out, strings.Repeat("界", 100)) {
		t.Fatalf("leading UTF-8 content was lost: %q", out)
	}
	if strings.TrimSpace(out) == strings.TrimSpace(body) {
		t.Fatalf("long output was not truncated: %q", out)
	}
}
