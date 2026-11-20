package backend

import (
	"path/filepath"
	"strings"
)

func parseBrowserProcessCommandLineArgs(commandLine string) []string {
	parts := splitProcessCommandLine(commandLine)
	if len(parts) <= 1 {
		return nil
	}
	return normalizeNonEmptyStrings(parts[1:])
}

func splitProcessCommandLine(commandLine string) []string {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return nil
	}
	parts := make([]string, 0, 16)
	var builder strings.Builder
	inQuote := false
	for _, char := range commandLine {
		if char == '"' {
			inQuote = !inQuote
			continue
		}
		if (char == ' ' || char == '\t' || char == '\r' || char == '\n') && !inQuote {
			if builder.Len() > 0 {
				parts = append(parts, builder.String())
				builder.Reset()
			}
			continue
		}
		builder.WriteRune(char)
	}
	if builder.Len() > 0 {
		parts = append(parts, builder.String())
	}
	return parts
}

func processCommandLineFlagValue(args []string, name string) string {
	for index, arg := range args {
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, name+"="))
		}
		if arg == name && index+1 < len(args) {
			return strings.TrimSpace(args[index+1])
		}
	}
	return ""
}

func normalizeProcessUserDataDir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	return filepath.Clean(value)
}
