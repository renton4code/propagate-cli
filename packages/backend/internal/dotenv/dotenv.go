package dotenv

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadDefault() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return LoadForWorkdir(cwd)
}

func LoadForWorkdir(workdir string) error {
	for _, path := range candidateFiles(workdir) {
		if err := loadFile(path); err != nil {
			return err
		}
	}
	return nil
}

func loadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		name, value, ok := parseLine(scanner.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func parseLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	name, rawValue, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return "", "", false
	}
	rawValue = strings.TrimSpace(rawValue)
	if strings.HasPrefix(rawValue, "\"") || strings.HasPrefix(rawValue, "'") {
		if value, err := strconv.Unquote(rawValue); err == nil {
			return name, value, true
		}
	}
	if value, _, found := strings.Cut(rawValue, " #"); found {
		rawValue = value
	}
	return name, strings.TrimSpace(rawValue), true
}

func candidateFiles(workdir string) []string {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	for dir := abs; ; dir = filepath.Dir(dir) {
		add(filepath.Join(dir, ".env"))
		add(filepath.Join(dir, "packages", "backend", ".env"))
		if isProjectBoundary(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return out
}

func isProjectBoundary(dir string) bool {
	for _, name := range []string{"go.work", "go.mod", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
