package terminal

import (
	"reflect"
	"testing"
)

func TestSlashCompletion(t *testing.T) {
	input := &Input{completing: true}
	c := slashCompleter{input: input}
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"/hel", []string{"p"}},
		{"/model ", []string{"auto"}},
		{"/s", []string{"essions", "tatus"}},
		{"/unknown", nil},
	} {
		line := []rune(tc.line)
		got, n := c.Do(line, len(line))
		var values []string
		for _, item := range got {
			values = append(values, string(item))
		}
		if n != len(line) || !reflect.DeepEqual(values, tc.want) {
			t.Errorf("completion %q = %v (%d), want %v", tc.line, values, n, tc.want)
		}
	}
}
func TestCompletionLeavesRegularAndContinuationInputAlone(t *testing.T) {
	input := &Input{completing: true}
	c := slashCompleter{input: input}
	for _, line := range []string{"hello", "代码", ""} {
		got, _ := c.Do([]rune(line), len([]rune(line)))
		if len(got) != 1 || string(got[0]) != "\t" {
			t.Fatalf("%q: %q", line, got)
		}
	}
	input.completing = false
	got, _ := c.Do([]rune("/help"), 5)
	if len(got) != 1 || string(got[0]) != "\t" {
		t.Fatalf("continuation: %q", got)
	}
}
