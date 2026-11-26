package main

import (
	"fmt"
	"strconv"
	"strings"

	"kero/internal/session"
	"kero/internal/terminal"
)

// chooseSession lists only sessions belonging to the current workspace.
func chooseSession(input *terminal.Input, root string) (string, error) {
	sessions, err := session.List(root)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		fmt.Println("No saved sessions for this workspace.")
		return "", nil
	}
	fmt.Println("Sessions (newest first):")
	for i, s := range sessions {
		fmt.Printf("  %d. %s  %s  %s\n", i+1, s.ID, s.Updated.Local().Format("2006-01-02 15:04"), s.Preview)
	}
	for {
		text, err := input.Prompt("Select number (0 to cancel): ")
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || n > len(sessions) {
			fmt.Println("Invalid selection.")
			continue
		}
		if n == 0 {
			return "", nil
		}
		return sessions[n-1].ID, nil
	}
}
