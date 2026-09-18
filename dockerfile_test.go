package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestDockerfileFromArgsAreDeclaredGlobally(t *testing.T) {
	content, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	globalArgs := make(map[string]struct{})
	fromArgPattern := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	globalScope := true
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upperLine := strings.ToUpper(line)
		if strings.HasPrefix(upperLine, "FROM ") {
			for _, match := range fromArgPattern.FindAllStringSubmatch(line, -1) {
				if _, ok := globalArgs[match[1]]; !ok {
					t.Fatalf("Dockerfile FROM references ${%s} before a global ARG declaration", match[1])
				}
			}
			globalScope = false
			continue
		}
		if globalScope && strings.HasPrefix(upperLine, "ARG ") {
			argName := strings.TrimSpace(line[len("ARG "):])
			if name, _, ok := strings.Cut(argName, "="); ok {
				argName = strings.TrimSpace(name)
			}
			globalArgs[argName] = struct{}{}
			continue
		}

		if globalScope {
			// Once the first non-ARG instruction is reached, following ARG
			// values are stage-local and cannot satisfy FROM interpolation.
			break
		}
	}
}

func TestDockerfileUsesBinaryEntrypoint(t *testing.T) {
	content, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	if !strings.Contains(string(content), `ENTRYPOINT ["./ransomware-news-bot"]`) {
		t.Fatal("Dockerfile must set ENTRYPOINT to ./ransomware-news-bot so docker run image --flag invokes the bot")
	}
}
