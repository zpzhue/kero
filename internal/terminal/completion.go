package terminal

import "strings"

// slashCompleter completes only the first line of a conversation message.
type slashCompleter struct{ input *Input }

var slashCommands = []string{"/exit", "/help", "/model", "/model auto", "/models", "/resume", "/sessions", "/status"}

func (c slashCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if c.input == nil || !c.input.completing || pos != len(line) || pos == 0 || line[0] != '/' {
		return [][]rune{[]rune("\t")}, 0
	}
	prefix := string(line)
	var matches [][]rune
	for _, command := range slashCommands {
		if strings.HasPrefix(command, prefix) && len(command) > len(prefix) {
			matches = append(matches, []rune(command[len(prefix):]))
		}
	}
	return matches, len(line)
}
