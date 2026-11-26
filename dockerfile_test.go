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
			assertDockerfileFromArgsDeclaredGlobally(t, line, fromArgPattern, globalArgs)
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

func assertDockerfileFromArgsDeclaredGlobally(t *testing.T, line string, pattern *regexp.Regexp, globalArgs map[string]struct{}) {
	t.Helper()
	for _, match := range pattern.FindAllStringSubmatch(line, -1) {
		if _, ok := globalArgs[match[1]]; !ok {
			t.Fatalf("Dockerfile FROM references ${%s} before a global ARG declaration", match[1])
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

// dockerfileInstructions returns the Dockerfile's logical instructions with
// backslash line continuations joined and comment lines dropped.
func dockerfileInstructions(t *testing.T) []string {
	t.Helper()
	content, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	var instructions []string
	var current strings.Builder
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continued := strings.HasSuffix(line, `\`)
		current.WriteString(strings.TrimSpace(strings.TrimSuffix(line, `\`)))
		current.WriteString(" ")
		if !continued {
			instructions = append(instructions, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		instructions = append(instructions, strings.TrimSpace(current.String()))
	}
	return instructions
}

// dockerfileStages splits the logical instructions at every FROM. Element 0
// holds the global ARGs before the first FROM.
func dockerfileStages(instructions []string) [][]string {
	stages := [][]string{nil}
	for _, instruction := range instructions {
		if strings.HasPrefix(strings.ToUpper(instruction), "FROM ") {
			stages = append(stages, nil)
		}
		stages[len(stages)-1] = append(stages[len(stages)-1], instruction)
	}
	return stages
}

// The base images are referenced by the tag of their release line, never by a
// patch tag or digest, so every rebuild pulls the line's current security
// updates without a manual pin bump (operator dependency-currency rule).
func TestDockerfileBaseImagesUseLineTagsWithoutDigest(t *testing.T) {
	want := map[string]*regexp.Regexp{
		"GO_BASE_IMAGE":      regexp.MustCompile(`^golang:[0-9]+\.[0-9]+-alpine$`),
		"RUNTIME_BASE_IMAGE": regexp.MustCompile(`^alpine:[0-9]+\.[0-9]+$`),
	}
	found := make(map[string]string)
	for _, instruction := range dockerfileStages(dockerfileInstructions(t))[0] {
		name, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(instruction, "ARG ")), "=")
		if ok {
			found[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	for name, pattern := range want {
		value, ok := found[name]
		if !ok {
			t.Errorf("Dockerfile must declare a global ARG %s with a default base image", name)
			continue
		}
		if !pattern.MatchString(value) {
			t.Errorf("ARG %s=%q must be a line tag matching %s (no patch tag, no @sha256 digest)", name, value, pattern)
		}
	}
}

// Every stage installs the Alpine distribution's current security updates on
// each build through an unpinned apk upgrade, and no apk package carries a
// version pin.
func TestDockerfileStagesRunUnpinnedApkUpgrade(t *testing.T) {
	stages := dockerfileStages(dockerfileInstructions(t))[1:]
	if len(stages) < 2 {
		t.Fatalf("Dockerfile must have a builder and a runtime stage, found %d stage(s)", len(stages))
	}
	for _, stage := range stages {
		assertStageRunsApkUpgrade(t, stage)
	}
}

func assertStageRunsApkUpgrade(t *testing.T, stage []string) {
	t.Helper()
	upgraded := false
	for _, instruction := range stage {
		if !strings.HasPrefix(strings.ToUpper(instruction), "RUN ") {
			continue
		}
		if strings.Contains(instruction, "apk upgrade --no-cache") {
			upgraded = true
		}
		assertApkAddHasNoVersionPins(t, stage[0], instruction)
	}
	if !upgraded {
		t.Errorf("stage %q must run `apk upgrade --no-cache`", stage[0])
	}
}

func assertApkAddHasNoVersionPins(t *testing.T, stageName, instruction string) {
	t.Helper()
	_, packages, ok := strings.Cut(instruction, "apk add ")
	if !ok {
		return
	}
	packages, _, _ = strings.Cut(packages, "&&")
	for _, field := range strings.Fields(packages) {
		if !strings.HasPrefix(field, "-") && strings.ContainsAny(field, "=~<>") {
			t.Errorf("stage %q pins apk package %q; OS packages must stay unpinned", stageName, field)
		}
	}
}
