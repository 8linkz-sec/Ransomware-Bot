package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemovedDependenciesDoNotReappear(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(file)

	forbidden := []string{
		"github.com/bwmarrin/discord" + "go",
		"github.com/mmcdole/" + "gofeed",
		"github.com/json-iterator/" + "go",
		"gopkg.in/natefinch/lumber" + "jack.v2",
	}

	checkedFiles := []string{
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "go.sum"),
		filepath.Join(root, "main.go"),
		filepath.Join(root, "readme.md"),
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		checkedFiles = append(checkedFiles, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal package tree: %v", err)
	}

	for _, path := range checkedFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, module := range forbidden {
			if bytes.Contains(data, []byte(module)) {
				t.Fatalf("%s still contains removed dependency %q", path, module)
			}
		}
	}

	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list module graph: %v\n%s", err, output)
	}
	modules := strings.Fields(string(output))
	for _, module := range forbidden {
		for _, resolved := range modules {
			if resolved == module {
				t.Fatalf("module graph still contains removed dependency %q", module)
			}
		}
	}
}
