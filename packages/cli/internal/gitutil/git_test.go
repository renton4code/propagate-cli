package gitutil

import (
	"reflect"
	"testing"
)

func TestProjectDirsIncludesAnyTopLevelDirectory(t *testing.T) {
	worktree := Worktree{
		TrackedFiles: []string{
			"README.md",
			"apps/web/src/main.ts",
			"packages/cli/main.go",
			"services/api/server.go",
			"docs/intro.mdx",
			"node_modules/lib/index.js",
			"dist/app.js",
			"build/output.txt",
			"coverage/lcov.info",
			".github/workflows/ci.yml",
			"_generated/types.ts",
			"foo/.env",
			"foo/src/index.ts",
		},
	}

	got := ProjectDirs(worktree)
	want := []string{".", "apps", "docs", "foo", "packages", "services"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectDirs() = %v, want %v", got, want)
	}
}

func TestTopLevelDir(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "nested path", path: "apps/web/main.go", want: "apps"},
		{name: "root file", path: "README.md", want: ""},
		{name: "dot", path: ".", want: ""},
		{name: "trims spaces", path: "  docs/page.mdx  ", want: "docs"},
	}

	for _, tt := range tests {
		if got := topLevelDir(tt.path); got != tt.want {
			t.Fatalf("%s: topLevelDir(%q) = %q, want %q", tt.name, tt.path, got, tt.want)
		}
	}
}
