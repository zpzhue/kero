package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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
		definition("read_file", "Read a UTF-8 text file in the workspace, optionally selecting an inclusive line range.", map[string]any{
			"path":       str("Workspace-relative file path."),
			"start_line": map[string]any{"type": "integer", "minimum": 1, "description": "First line to read, counting from 1. Defaults to 1."},
			"end_line":   map[string]any{"type": "integer", "minimum": 1, "description": "Last line to read, counting from 1. Defaults to the end of the file."},
		}, "path"),
		definition("search", "Search workspace files line by line, skipping .git and paths excluded by .gitignore.", map[string]any{
			"path":   str("Workspace-relative file or directory; defaults to the workspace root."),
			"query":  str("Case-sensitive literal text or a Go regular expression."),
			"regexp": map[string]any{"type": "boolean", "description": "Treat query as a Go regular expression. Defaults to false."},
		}, "query"),
		definition("edit_file", "Replace one unique exact occurrence in a workspace file after reading it with read_file, or create a new file using empty old text.", map[string]any{"path": str("Workspace-relative file path."), "old": str("Exact existing text, or empty to create a new file."), "new": str("Replacement text.")}, "path", "old", "new"),
		definition("run_command", "Run a shell command with the workspace as its working directory.", map[string]any{"command": str("Shell command to run.")}, "command"),
	}
}

type arguments struct {
	Path      string  `json:"path"`
	Query     string  `json:"query"`
	Old       *string `json:"old"`
	New       *string `json:"new"`
	Command   string  `json:"command"`
	StartLine *int    `json:"start_line"`
	EndLine   *int    `json:"end_line"`
	Regexp    bool    `json:"regexp"`
}

// Workspace retains file snapshots for one session. Its zero value is ready to use.
type Workspace struct {
	mu        sync.Mutex
	snapshots map[string]fileSnapshot
}

// Execute runs a tool call using the current file contents for a standalone edit.
// Use Workspace.Execute to require a prior read and detect changes across calls.
func Execute(ctx context.Context, root, mode string, call provider.ToolCall, confirm func(string) bool) string {
	return (*Workspace)(nil).execute(ctx, root, mode, call, confirm)
}

// Execute runs a tool call with this session's file snapshots. Ask mode confirms
// edits and commands, workspace mode confirms commands, and auto confirms neither.
func (w *Workspace) Execute(ctx context.Context, root, mode string, call provider.ToolCall, confirm func(string) bool) string {
	return w.execute(ctx, root, mode, call, confirm)
}

func (w *Workspace) execute(ctx context.Context, root, mode string, call provider.ToolCall, confirm func(string) bool) string {
	if err := ctx.Err(); err != nil {
		return "Error: " + err.Error()
	}
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
		return w.read(fsroot, root, path, args.StartLine, args.EndLine)
	case "search":
		if args.Query == "" {
			return "Error: query is required"
		}
		query := []byte(args.Query)
		match := func(line []byte) bool { return bytes.Contains(line, query) }
		if args.Regexp {
			pattern, err := regexp.Compile(args.Query)
			if err != nil {
				return "Error: invalid regular expression: " + err.Error()
			}
			match = pattern.Match
		}
		return search(ctx, fsroot, root, path, match)
	case "edit_file":
		if args.Old == nil || args.New == nil {
			return "Error: old and new are required"
		}
		return w.edit(ctx, fsroot, root, path, args.Path, *args.Old, *args.New, mode, confirm)
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
	end := maxOutput
	for !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "\n[output truncated]"
}

func (w *Workspace) read(fsroot *os.Root, root, path string, start, end *int) string {
	if start != nil && *start < 1 || end != nil && *end < 1 {
		return "Error: start_line and end_line must be positive"
	}
	if start != nil && end != nil && *start > *end {
		return "Error: start_line must be at or before end_line"
	}
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
	}
	data, info, err := readFile(fsroot, root, path)
	if err != nil {
		if w != nil && errors.Is(err, os.ErrNotExist) {
			delete(w.snapshots, path)
		}
		return "Error: " + err.Error()
	}
	if !utf8.Valid(data) {
		return "Error: file is not UTF-8"
	}
	selected := data
	if start != nil || end != nil {
		if start != nil {
			selected = skipLines(data, *start-1)
			if *start > 1 && len(selected) == 0 {
				return "Error: start_line is past the end of the file"
			}
		}
		if end != nil {
			count := *end
			if start != nil {
				count -= *start - 1
			}
			selected = selected[:len(selected)-len(skipLines(selected, count))]
		}
	}
	if w != nil {
		if w.snapshots == nil {
			w.snapshots = make(map[string]fileSnapshot)
		}
		w.snapshots[path] = snapshot(data, info)
	}
	return bounded(string(selected))
}

func skipLines(data []byte, count int) []byte {
	for count > 0 && len(data) > 0 {
		end := bytes.IndexAny(data, "\r\n")
		if end < 0 {
			return nil
		}
		next := end + 1
		if data[end] == '\r' && next < len(data) && data[next] == '\n' {
			next++
		}
		data = data[next:]
		count--
	}
	return data
}

type fileSnapshot struct {
	info os.FileInfo
	sum  [sha256.Size]byte
}

func snapshot(data []byte, info os.FileInfo) fileSnapshot {
	return fileSnapshot{info: info, sum: sha256.Sum256(data)}
}

func (s fileSnapshot) matches(other fileSnapshot) bool {
	return s.sum == other.sum && s.info.Mode() == other.info.Mode() && os.SameFile(s.info, other.info)
}

func readFile(fsroot *os.Root, root, path string) ([]byte, os.FileInfo, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, nil, err
	}
	f, err := fsroot.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	if info.Size() > maxFile {
		return nil, nil, errors.New("file exceeds 4 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxFile {
		return nil, nil, errors.New("file exceeds 4 MiB limit")
	}
	return data, info, nil
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

func search(ctx context.Context, fsroot *os.Root, root, path string, match func([]byte) bool) string {
	var out strings.Builder
	start, err := filepath.Rel(root, path)
	if err != nil {
		return "Error: " + err.Error()
	}
	start = filepath.ToSlash(start)
	var ignores []ignoreRule
	for dir := "."; dir != start; {
		ignores = loadIgnoreRules(fsroot, dir, ignores)
		remaining := start
		if dir != "." {
			remaining = start[len(dir)+1:]
		}
		part, rest, _ := strings.Cut(remaining, "/")
		dir = filepath.ToSlash(filepath.Join(dir, part))
		if part == ".git" || (rest != "" && ignored(ignores, dir, true)) {
			return ""
		}
		if rest == "" {
			break
		}
	}
	err = fs.WalkDir(fsroot.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		for len(ignores) > 0 && !strings.HasPrefix(p, ignores[len(ignores)-1].base) {
			ignores = ignores[:len(ignores)-1]
		}
		if d.Name() == ".git" || ignored(ignores, p, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			ignores = loadIgnoreRules(fsroot, p, ignores)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, _, err := readFile(fsroot, root, filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxFile+1)
		for line := 1; scanner.Scan(); line++ {
			if match(scanner.Bytes()) {
				fmt.Fprintf(&out, "%s:%d:%s\n", p, line, scanner.Bytes())
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

func (w *Workspace) edit(ctx context.Context, fsroot *os.Root, root, path, requested, old, replacement, mode string, confirm func(string) bool) string {
	if !utf8.ValidString(replacement) {
		return "Error: replacement must be UTF-8"
	}
	original, info, err := readFile(fsroot, root, path)
	creating := errors.Is(err, os.ErrNotExist)
	if err != nil && !creating {
		return "Error: " + err.Error()
	}
	var before fileSnapshot
	if !creating {
		before = snapshot(original, info)
	}
	if w != nil {
		w.mu.Lock()
		saved, read := w.snapshots[path]
		w.mu.Unlock()
		if read && (creating || !saved.matches(before)) {
			return "Error: edit conflict: file changed since read_file; read the file again"
		}
		if !read && !creating {
			return "Error: read the file with read_file before editing it"
		}
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
	newText := []byte(replacement)
	updated := newText
	if !creating {
		oldText := []byte(old)
		if bytes.Count(original, oldText) != 1 {
			return "Error: old text must occur exactly once; file was not changed"
		}
		updated = bytes.Replace(original, oldText, newText, 1)
	}
	if len(updated) > maxFile {
		return "Error: edited file exceeds 4 MiB limit"
	}
	oldNewlines, newNewlines := newlineKinds(original), newlineKinds(updated)
	if newNewlines != 0 && (newNewlines&(newNewlines-1) != 0 || (oldNewlines != 0 && oldNewlines != newNewlines)) {
		return "Error: edit would change or mix newline styles"
	}
	rel, _ := filepath.Rel(root, path)
	label := filepath.ToSlash(rel)
	oldLabel := label
	if creating {
		oldLabel = "/dev/null"
	}
	preview := bounded("--- " + oldLabel + "\n+++ " + label + "\n" + diffLines(old, replacement))
	if mode == "ask" && (confirm == nil || !confirm("Apply edit?\n"+preview)) {
		return "Error: edit denied"
	}
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return "Error: " + err.Error()
	}
	again, err := workspacePath(root, requested)
	if err != nil || again != path {
		return "Error: edit conflict: path changed"
	}
	if creating {
		err = createFile(fsroot, rel, updated)
	} else {
		var after fileSnapshot
		after, err = replaceFile(ctx, fsroot, root, path, rel, before, updated)
		if err == nil && w != nil {
			w.snapshots[path] = after
		}
	}
	if err != nil {
		return "Error: " + err.Error()
	}
	return "Edit applied.\n" + preview
}

func createFile(root *os.Root, rel string, data []byte) error {
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if errors.Is(err, os.ErrExist) {
		return errors.New("edit conflict: file already exists")
	}
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		if current, lookupErr := root.Lstat(rel); statErr == nil && lookupErr == nil && os.SameFile(info, current) {
			err = errors.Join(err, root.Remove(rel))
		}
		return err
	}
	return nil
}

func replaceFile(ctx context.Context, fsroot *os.Root, root, path, rel string, before fileSnapshot, data []byte) (fileSnapshot, error) {
	dir, err := fsroot.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return fileSnapshot{}, err
	}
	defer dir.Close()
	temp := ".kero-edit-" + rand.Text()
	f, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fileSnapshot{}, err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = dir.Remove(temp)
		}
	}()
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(before.info.Mode().Perm())
	info, statErr := f.Stat()
	if err := errors.Join(writeErr, chmodErr, statErr, f.Close()); err != nil {
		return fileSnapshot{}, err
	}
	current, currentInfo, err := readFile(fsroot, root, path)
	if err != nil || !before.matches(snapshot(current, currentInfo)) {
		return fileSnapshot{}, errors.New("edit conflict: file changed; read the file again")
	}
	if err := ctx.Err(); err != nil {
		return fileSnapshot{}, err
	}
	// Go 1.24 lacks os.Root.Rename. Verify the parent and both entries
	// before publishing the staged file with a same-directory rename.
	if resolved, err := workspacePath(root, rel); err != nil || resolved != path {
		return fileSnapshot{}, errors.New("edit conflict: path changed")
	}
	stagedDir, dirErr := dir.Stat(".")
	currentDir, currentDirErr := os.Stat(filepath.Dir(path))
	stagedPath := filepath.Join(filepath.Dir(path), temp)
	currentTemp, tempErr := os.Lstat(stagedPath)
	destination, destErr := os.Lstat(path)
	if dirErr != nil || currentDirErr != nil || tempErr != nil || destErr != nil ||
		!os.SameFile(stagedDir, currentDir) || !os.SameFile(info, currentTemp) ||
		!destination.Mode().IsRegular() || !os.SameFile(before.info, destination) {
		return fileSnapshot{}, errors.New("edit conflict: path changed")
	}
	if destination.Size() != currentInfo.Size() || !destination.ModTime().Equal(currentInfo.ModTime()) {
		return fileSnapshot{}, errors.New("edit conflict: file changed; read the file again")
	}
	if err := os.Rename(stagedPath, path); err != nil {
		return fileSnapshot{}, err
	}
	removeTemp = false
	return snapshot(data, info), nil
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
