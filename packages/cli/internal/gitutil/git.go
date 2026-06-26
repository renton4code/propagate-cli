package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Worktree struct {
	Root         string
	TrackedFiles []string
	TrackedDirs  map[string]bool
}

func Discover(workdir string) (Worktree, error) {
	root, err := Root(workdir)
	if err != nil {
		return Worktree{}, err
	}
	files, err := ListTrackedFiles(root)
	if err != nil {
		return Worktree{}, err
	}
	return Worktree{
		Root:         root,
		TrackedFiles: files,
		TrackedDirs:  trackedDirs(files),
	}, nil
}

func Root(workdir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no Git repository detected; Propagate MVP project setup requires a Git worktree")
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("Git returned an empty worktree root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func ListTrackedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Git-tracked files: %w", err)
	}
	parts := bytes.Split(out, []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(part)))
	}
	sort.Strings(files)
	return files, nil
}

func RepoRelative(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return ".", nil
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside Git worktree %s", path, root)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func ProjectDirs(w Worktree) []string {
	dirs := map[string]bool{".": true}
	for _, file := range w.TrackedFiles {
		root := topLevelDir(file)
		if root == "" || isExcludedRootDir(root) {
			continue
		}
		dirs[root] = true
	}

	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func trackedDirs(files []string) map[string]bool {
	dirs := map[string]bool{".": true}
	for _, file := range files {
		dir := filepath.ToSlash(filepath.Dir(file))
		for dir != "." && dir != "/" {
			dirs[dir] = true
			dir = filepath.ToSlash(filepath.Dir(dir))
		}
		dirs["."] = true
	}
	return dirs
}

func topLevelDir(path string) string {
	clean := strings.TrimSpace(filepath.ToSlash(path))
	if clean == "" || clean == "." {
		return ""
	}
	first, _, found := strings.Cut(clean, "/")
	if !found {
		return ""
	}
	return first
}

func isExcludedRootDir(dir string) bool {
	if strings.HasPrefix(dir, ".") || strings.HasPrefix(dir, "_") {
		return true
	}
	switch dir {
	case "node_modules", "dist", "build", "coverage":
		return true
	default:
		return false
	}
}
