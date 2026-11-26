package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"kero/internal/provider"
)

const maxOutput = 64 * 1024
const maxFile = 4 * 1024 * 1024

// Definitions returns the tools available to the model.
func Definitions() []provider.Tool {
	definition := func(name, description string, properties map[string]any, required ...string) provider.Tool {
		return provider.Tool{Type: "function", Function: provider.ToolDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
	}
	str := func(description string) any { return map[string]any{"type": "string", "description": description} }
	return []provider.Tool{
		definition("list_files", "List files and directories under a workspace path, recursively.", map[string]any{"path": str("Workspace-relative directory; defaults to the workspace root.")}),
		definition("read_file", "Read a UTF-8 text file in the workspace.", map[string]any{"path": str("Workspace-relative file path.")}, "path"),
		definition("search", "Search workspace files for a literal, case-sensitive string.", map[string]any{"path": str("Workspace-relative file or directory; defaults to the workspace root."), "query": str("Literal text to find.")}, "query"),
		definition("edit_file", "Replace one unique exact occurrence in a workspace file, or create a new file using empty old text.", map[string]any{"path": str("Workspace-relative file path."), "old": str("Exact existing text, or empty to create a new file."), "new": str("Replacement text.")}, "path", "old", "new"),
		definition("run_command", "Run a shell command with the workspace as its working directory.", map[string]any{"command": str("Shell command to run.")}, "command"),
	}
}

type arguments struct {
	Path    string  `json:"path"`
	Query   string  `json:"query"`
	Old     *string `json:"old"`
	New     *string `json:"new"`
	Command string  `json:"command"`
}

// Execute runs a tool call. Confirmation is required according to mode: ask
// confirms edits and commands; workspace confirms commands; auto confirms neither.
func Execute(ctx context.Context, root, mode string, call provider.ToolCall, confirm func(string) bool) string {
	if mode != "ask" && mode != "workspace" && mode != "auto" {
		return "Error: invalid permission mode"
	}
	var args arguments
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "Error: invalid arguments: " + err.Error()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "Error: invalid arguments: expected one JSON object"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "Error: " + err.Error()
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "Error: workspace: " + err.Error()
	}
	name := call.Function.Name
	if name == "run_command" {
		if strings.TrimSpace(args.Command) == "" {
			return "Error: command is required"
		}
		if mode != "auto" && (confirm == nil || !confirm("Run command in "+root+":\n"+args.Command)) {
			return "Error: command denied"
		}
		return command(ctx, root, args.Command)
	}
	if name != "list_files" && name != "read_file" && name != "search" && name != "edit_file" {
		return "Error: unknown tool: " + name
	}
	if (name == "read_file" || name == "edit_file") && args.Path == "" {
		return "Error: path is required"
	}
	path, err := workspacePath(root, args.Path)
	if err != nil {
		return "Error: " + err.Error()
	}
	fsroot, err := os.OpenRoot(root)
	if err != nil {
		return "Error: workspace: " + err.Error()
	}
	defer fsroot.Close()
	switch name {
	case "list_files":
		return list(fsroot, root, path)
	case "read_file":
		data, err := readFile(fsroot, root, path)
		if err != nil {
			return "Error: " + err.Error()
		}
		return bounded(string(data))
	case "search":
		if args.Query == "" {
			return "Error: query is required"
		}
		return search(fsroot, root, path, args.Query)
	case "edit_file":
		if args.Old == nil || args.New == nil {
			return "Error: old and new are required"
		}
		return edit(fsroot, root, path, *args.Old, *args.New, mode, confirm)
	}
	return "Error: unknown tool"
}

// workspacePath resolves every existing path component before checking containment.
func workspacePath(root, path string) (string, error) {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", errors.New("path must be relative to the workspace")
	}
	joined := filepath.Join(root, path)
	resolved, err := filepath.EvalSymlinks(joined)
	if errors.Is(err, os.ErrNotExist) {
		// Only the final component may be missing; reject dangling symlinks.
		if _, statErr := os.Lstat(joined); statErr == nil {
			return "", errors.New("path is a dangling symlink")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(joined))
		if parentErr != nil {
			return "", parentErr
		}
		resolved = filepath.Join(parent, filepath.Base(joined))
	} else if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path is outside the workspace")
	}
	return resolved, nil
}

func bounded(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n[output truncated]"
}

func readFile(fsroot *os.Root, root, path string) ([]byte, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	f, err := fsroot.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxFile {
		return nil, errors.New("file exceeds 4 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFile {
		return nil, errors.New("file exceeds 4 MiB limit")
	}
	return data, nil
}

func list(fsroot *os.Root, root, path string) string {
	var out strings.Builder
	start, err := filepath.Rel(root, path)
	if err != nil {
		return "Error: " + err.Error()
	}
	err = fs.WalkDir(fsroot.FS(), filepath.ToSlash(start), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if out.Len() >= maxOutput {
			return fs.SkipAll
		}
		rel := p
		if rel == "." {
			return nil
		}
		out.WriteString(filepath.ToSlash(rel))
		if d.IsDir() {
			out.WriteString("/")
		} else if d.Type()&os.ModeSymlink != 0 {
			out.WriteString(" -> [symlink]")
		}
		out.WriteByte('\n')
		return nil
	})
	if err != nil {
		return "Error: " + err.Error()
	}
	return bounded(out.String())
}

func search(fsroot *os.Root, root, path, query string) string {
	var out strings.Builder
	start, err := filepath.Rel(root, path)
	if err != nil {
		return "Error: " + err.Error()
	}
	err = fs.WalkDir(fsroot.FS(), filepath.ToSlash(start), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if out.Len() >= maxOutput {
			return fs.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := readFile(fsroot, root, filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel := p
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxFile+1)
		for line := 1; scanner.Scan(); line++ {
			if strings.Contains(scanner.Text(), query) {
				fmt.Fprintf(&out, "%s:%d:%s\n", filepath.ToSlash(rel), line, scanner.Text())
				if out.Len() >= maxOutput {
					return fs.SkipAll
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return "Error: " + err.Error()
	}
	return bounded(out.String())
}

func edit(fsroot *os.Root, root, path, old, replacement, mode string, confirm func(string) bool) string {
	if !utf8.ValidString(replacement) {
		return "Error: replacement must be UTF-8"
	}
	original, err := readFile(fsroot, root, path)
	creating := errors.Is(err, os.ErrNotExist)
	if err != nil && !creating {
		return "Error: " + err.Error()
	}
	if creating && old != "" {
		return "Error: file does not exist; use empty old text to create it"
	}
	if !creating && old == "" {
		return "Error: empty old text can only create a new file"
	}
	if !utf8.Valid(original) {
		return "Error: file is not UTF-8"
	}
	var updated []byte
	if creating {
		updated = []byte(replacement)
	} else {
		if bytes.Count(original, []byte(old)) != 1 {
			return "Error: old text must occur exactly once; file was not changed"
		}
		updated = bytes.Replace(original, []byte(old), []byte(replacement), 1)
	}
	if len(updated) > maxFile {
		return "Error: edited file exceeds 4 MiB limit"
	}
	before, after := newlineKinds(original), newlineKinds(updated)
	if after != 0 && (after&(after-1) != 0 || (before != 0 && before != after)) {
		return "Error: edit would change or mix newline styles"
	}
	rel, _ := filepath.Rel(root, path)
	preview := "--- " + filepath.ToSlash(rel) + "\n+++ " + filepath.ToSlash(rel) + "\n" + diffLines(old, replacement)
	if creating {
		preview = "--- /dev/null\n+++ " + filepath.ToSlash(rel) + "\n" + diffLines("", replacement)
	}
	if mode == "ask" && (confirm == nil || !confirm("Apply edit?\n"+bounded(preview))) {
		return "Error: edit denied"
	}
	again, err := workspacePath(root, rel)
	if err != nil || again != path {
		return "Error: edit conflict: path changed"
	}
	flags := os.O_WRONLY | os.O_TRUNC
	if creating {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	} else {
		current, err := readFile(fsroot, root, path)
		if err != nil || !bytes.Equal(current, original) {
			return "Error: edit conflict: file changed"
		}
	}
	f, err := fsroot.OpenFile(rel, flags, 0666)
	if err != nil {
		if creating && errors.Is(err, os.ErrExist) {
			return "Error: edit conflict: file already exists"
		}
		return "Error: " + err.Error()
	}
	_, err = f.Write(updated)
	closeErr := f.Close()
	if err != nil {
		return "Error: " + err.Error()
	}
	if closeErr != nil {
		return "Error: " + closeErr.Error()
	}
	return "Edit applied.\n" + bounded(preview)
}

// newlineKinds identifies LF, CRLF, and standalone CR without normalizing them.
func newlineKinds(data []byte) uint8 {
	var kinds uint8
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\r':
			if i+1 < len(data) && data[i+1] == '\n' {
				kinds |= 2
				i++
			} else {
				kinds |= 4
			}
		case '\n':
			kinds |= 1
		}
	}
	return kinds
}

func diffLines(old, replacement string) string {
	var out strings.Builder
	for _, line := range strings.Split(old, "\n") {
		out.WriteString("-" + line + "\n")
	}
	for _, line := range strings.Split(replacement, "\n") {
		out.WriteString("+" + line + "\n")
	}
	return out.String()
}

type cappedWriter struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutput - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
		} else {
			_, _ = w.buf.Write(p)
		}
	}
	if n > remaining {
		w.truncated = true
	}
	return n, nil
}

func command(ctx context.Context, root, command string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = root
	var output cappedWriter
	cmd.Stdout = &output
	cmd.WaitDelay = 2 * time.Second
	cmd.Stderr = &output
	err := cmd.Run()
	result := output.buf.String()
	if output.truncated {
		result += "\n[output truncated]"
	}
	if ctx.Err() != nil {
		return result + "\nError: " + ctx.Err().Error()
	}
	if err != nil {
		return result + "\nError: " + err.Error()
	}
	return result
}
