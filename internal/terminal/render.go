package terminal

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Renderer writes a plain-text conversation to a terminal or redirected output.
// Its methods are called in conversation order by a single caller.
type Renderer struct {
	w        io.Writer
	started  bool
	lineOpen bool
	pending  [utf8.UTFMax]byte
	pendingN int
}

// NewRenderer creates a renderer that writes to w.
func NewRenderer(w io.Writer) *Renderer {
	return &Renderer{w: w}
}

// StartReply prepares a new reply. Its heading is deferred until text arrives.
func (r *Renderer) StartReply() {
	r.started = false
	r.lineOpen = false
	r.pendingN = 0
}

// WriteReply displays a streamed fragment, retaining only an incomplete rune or
// a trailing carriage return for the next fragment.
func (r *Renderer) WriteReply(s string) {
	if s == "" {
		return
	}
	if !r.started {
		r.write("助手\n")
		r.started = true
	}

	for r.pendingN != 0 && s != "" {
		n := copy(r.pending[r.pendingN:], s)
		s = s[n:]
		r.pendingN += n
		text, consumed := safePrefix(string(r.pending[:r.pendingN]), false)
		r.writeReplyText(text)
		r.pendingN = copy(r.pending[:], r.pending[consumed:r.pendingN])
	}
	if s != "" {
		text, consumed := safePrefix(s, false)
		r.writeReplyText(text)
		r.pendingN = copy(r.pending[:], s[consumed:])
	}
}

// EndReply finishes an open output line and reports whether the reply had text.
func (r *Renderer) EndReply() bool {
	if r.pendingN != 0 {
		r.writeReplyText(Safe(string(r.pending[:r.pendingN])))
	}
	if r.lineOpen {
		r.write("\n")
	}
	started := r.started
	r.StartReply()
	return started
}

// ToolResult displays a bounded preview of the result.
func (r *Renderer) ToolResult(result string) {
	const (
		maxLines = 12
		maxRunes = 2000
	)

	r.write("结果\n")
	if result == "" {
		r.write("  （无输出）\n")
		return
	}

	end, lines, runes := 0, 0, 0
	for end < len(result) && lines < maxLines && runes < maxRunes {
		ch, size := terminalRune(result[end:], true)
		width := 1
		if b, escape := escapedByte(ch, size, result[end]); escape {
			width = len(byteEscape(b))
		}
		if runes+width > maxRunes {
			break
		}
		runes += width
		end += size
		if ch == '\n' {
			lines++
		}
	}

	preview := strings.TrimSuffix(Safe(result[:end]), "\n")
	for line := range strings.SplitSeq(preview, "\n") {
		r.write("  ")
		r.write(line)
		r.write("\n")
	}
	if end < len(result) {
		r.write("（输出过长，终端仅显示部分内容；完整结果已提供给模型并保存到会话。）\n")
	}
}

func (r *Renderer) writeReplyText(s string) {
	if s != "" {
		r.write(s)
		r.lineOpen = s[len(s)-1] != '\n'
	}
}

func (r *Renderer) write(s string) {
	_, _ = io.WriteString(r.w, s)
}

// Safe escapes terminal controls and invalid UTF-8 bytes for display, retaining
// tabs and line feeds and normalizing CRLF to LF.
func Safe(s string) string {
	text, _ := safePrefix(s, true)
	return text
}

func safePrefix(s string, final bool) (string, int) {
	var out strings.Builder
	start, end := 0, 0
	for end < len(s) {
		ch, size := terminalRune(s[end:], final)
		if size == 0 {
			break
		}
		var replacement string
		if ch == '\n' && size == 2 {
			replacement = "\n"
		} else if b, escape := escapedByte(ch, size, s[end]); escape {
			replacement = byteEscape(b)
		}
		if replacement != "" {
			if out.Len() == 0 {
				out.Grow(len(s))
			}
			out.WriteString(s[start:end])
			out.WriteString(replacement)
			start = end + size
		}
		end += size
	}
	if start == 0 {
		return s[:end], end
	}
	out.WriteString(s[start:end])
	return out.String(), end
}

// terminalRune consumes one rune or a CRLF pair. Size zero means more input is
// required to tell what the last bytes represent.
func terminalRune(s string, final bool) (rune, int) {
	if s[0] == '\r' {
		if len(s) >= 2 && s[1] == '\n' {
			return '\n', 2
		}
		if len(s) == 1 && !final {
			return 0, 0
		}
	}
	if s[0] < utf8.RuneSelf {
		return rune(s[0]), 1
	}
	if !final && !utf8.FullRuneInString(s) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(s)
}

func escapedByte(ch rune, size int, first byte) (byte, bool) {
	if ch == utf8.RuneError && size == 1 {
		return first, true
	}
	if ch == '\n' || ch == '\t' {
		return 0, false
	}
	return byte(ch), ch < ' ' || (ch >= 0x7f && ch <= 0x9f)
}

func byteEscape(b byte) string {
	switch b {
	case '\b':
		return `\b`
	case '\r':
		return `\r`
	default:
		const hex = "0123456789abcdef"
		return string([]byte{'\\', 'x', hex[b>>4], hex[b&15]})
	}
}
