package tools

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type ignoreRule struct {
	base    string
	pattern *regexp.Regexp
	dirOnly bool
	negated bool
}

func loadIgnoreRules(root *os.Root, dir string, rules []ignoreRule) []ignoreRule {
	name := filepath.FromSlash(path.Join(dir, ".gitignore"))
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return rules
	}
	data, _, err := readFile(root, root.Name(), filepath.Join(root.Name(), name))
	if err != nil {
		return rules
	}
	base := ""
	if dir != "." {
		base = dir + "/"
	}
	scanner := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(data, []byte("\ufeff"))))
	scanner.Buffer(nil, maxFile+1)
	for scanner.Scan() {
		line := trimIgnoreSpaces(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		pattern, negated := strings.CutPrefix(line, "!")
		pattern, dirOnly := strings.CutSuffix(pattern, "/")
		if pattern == "" {
			continue
		}
		expression, err := regexp.Compile(ignoreExpression(pattern))
		if err == nil {
			rules = append(rules, ignoreRule{base: base, pattern: expression, dirOnly: dirOnly, negated: negated})
		}
	}
	return rules
}

func trimIgnoreSpaces(line string) string {
	for len(line) > 0 && line[len(line)-1] == ' ' {
		backslash := len(line) - 2
		for backslash >= 0 && line[backslash] == '\\' {
			backslash--
		}
		if (len(line)-2-backslash)%2 != 0 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

func ignored(rules []ignoreRule, name string, isDir bool) bool {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		if rel, ok := strings.CutPrefix(name, rule.base); ok && (!rule.dirOnly || isDir) && rule.pattern.MatchString(rel) {
			return !rule.negated
		}
	}
	return false
}

func ignoreExpression(pattern string) string {
	var expression strings.Builder
	expression.WriteString(`(?s)^`)
	if !strings.Contains(pattern, "/") {
		expression.WriteString(`(?:.*/)?`)
	}
	pattern = strings.TrimPrefix(pattern, "/")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			end := i + 1
			for end < len(pattern) && pattern[end] == '*' {
				end++
			}
			if end-i >= 2 && (i == 0 || pattern[i-1] == '/') {
				if end == len(pattern) && i > 0 {
					expression.WriteString(`.*`)
				} else if end < len(pattern) && pattern[end] == '/' {
					expression.WriteString(`(?:.*/)?`)
					end++
				} else {
					expression.WriteString(`[^/]*`)
				}
			} else {
				expression.WriteString(`[^/]*`)
			}
			i = end - 1
		case '?':
			expression.WriteString(`[^/]`)
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 1 {
				expression.WriteString(`\[`)
				continue
			}
			end += i + 1
			class := pattern[i+1 : end]
			expression.WriteByte('[')
			if class[0] == '!' || class[0] == '^' {
				expression.WriteString("^/")
				class = class[1:]
			}
			expression.WriteString(class)
			expression.WriteByte(']')
			i = end
		case '\\':
			if i+1 < len(pattern) {
				i++
			}
			fallthrough
		default:
			if strings.ContainsRune(`\.+()|[]{}^$*?`, rune(pattern[i])) {
				expression.WriteByte('\\')
			}
			expression.WriteByte(pattern[i])
		}
	}
	expression.WriteByte('$')
	return expression.String()
}
