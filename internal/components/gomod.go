package components

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

type parsedGoMod struct {
	modulePath string
	goVersion  string
	requires   []DeclaredModule
	moduleLine int
}

func parseGoMod(filePath string, content []byte) (parsedGoMod, error) {
	parsed := parsedGoMod{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	inRequireBlock := false
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		clean, _ := stripLineCommentKeepIndirect(line)
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}

		switch {
		case !inRequireBlock && strings.HasPrefix(clean, "module "):
			fields := strings.Fields(clean)
			if len(fields) != 2 {
				return parsedGoMod{}, fmt.Errorf("%s: malformed module directive", filePath)
			}
			parsed.modulePath = unquote(fields[1])
			parsed.moduleLine = lineNumber

		case !inRequireBlock && strings.HasPrefix(clean, "go ") &&
			!strings.HasPrefix(clean, "go mod ") && !strings.HasPrefix(clean, "go toolchain "):
			fields := strings.Fields(clean)
			if len(fields) != 2 {
				return parsedGoMod{}, fmt.Errorf("%s: malformed go directive", filePath)
			}
			parsed.goVersion = fields[1]

		case !inRequireBlock && (clean == "require" || strings.HasPrefix(clean, "require (")):
			inRequireBlock = true

		case !inRequireBlock && strings.HasPrefix(clean, "require "):
			requirement, ok := parseRequirement(line)
			if !ok {
				return parsedGoMod{}, fmt.Errorf("%s: malformed require directive", filePath)
			}
			parsed.requires = append(parsed.requires, requirement)

		case inRequireBlock && clean == ")":
			inRequireBlock = false

		case inRequireBlock:
			requirement, ok := parseRequirement(line)
			if !ok {
				return parsedGoMod{}, fmt.Errorf("%s: malformed require entry", filePath)
			}
			parsed.requires = append(parsed.requires, requirement)
		}
	}
	if err := scanner.Err(); err != nil {
		return parsedGoMod{}, fmt.Errorf("%s: read go.mod: %w", filePath, err)
	}

	if parsed.modulePath == "" {
		return parsedGoMod{}, fmt.Errorf("%s: no module directive", filePath)
	}
	return parsed, nil
}

func parseRequirement(line string) (DeclaredModule, bool) {
	_, indirect := stripLineCommentKeepIndirect(line)
	text, _ := stripLineCommentKeepIndirect(line)
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "(") {
		text = strings.TrimPrefix(text, "(")
		text = strings.TrimSpace(text)
	}
	if strings.HasSuffix(text, ")") {
		text = strings.TrimSuffix(text, ")")
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return DeclaredModule{}, false
	}
	if strings.HasPrefix(text, "require ") {
		text = strings.TrimPrefix(text, "require ")
		text = strings.TrimSpace(text)
	}
	fields := strings.Fields(text)
	if len(fields) != 2 {
		return DeclaredModule{}, false
	}
	return DeclaredModule{
		Path:     unquote(fields[0]),
		Version:  fields[1],
		Indirect: indirect,
	}, true
}

func stripLineCommentKeepIndirect(line string) (string, bool) {
	orig := line
	if idx := strings.Index(orig, "//"); idx >= 0 {
		rest := strings.TrimSpace(orig[idx+2:])
		r := strings.ToLower(rest)
		indirect := r == "indirect" || strings.HasPrefix(r, "indirect ") || strings.Contains(r, " indirect") || strings.HasPrefix(r, "(indirect)") || strings.Contains(r, "indirect)")
		return strings.TrimSpace(orig[:idx]), indirect
	}
	return strings.TrimSpace(orig), false
}

func unquote(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '`' && value[len(value)-1] == '`') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
