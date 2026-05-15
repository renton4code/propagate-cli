package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadForWorkdirLoadsBackendDotenv(t *testing.T) {
	t.Setenv("PROPAGATE_DATABASE_URL", "")
	if err := os.Unsetenv("PROPAGATE_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("PORT"); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	backendDir := filepath.Join(root, "packages", "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	envText := "PROPAGATE_DATABASE_URL=postgres://local\nPORT=9090\n"
	if err := os.WriteFile(filepath.Join(backendDir, ".env"), []byte(envText), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadForWorkdir(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PROPAGATE_DATABASE_URL"); got != "postgres://local" {
		t.Fatalf("PROPAGATE_DATABASE_URL = %q", got)
	}
	if got := os.Getenv("PORT"); got != "9090" {
		t.Fatalf("PORT = %q", got)
	}
}

func TestLoadForWorkdirDoesNotOverrideExistingEnvironment(t *testing.T) {
	t.Setenv("PROPAGATE_DATABASE_URL", "postgres://exported")

	root := t.TempDir()
	backendDir := filepath.Join(root, "packages", "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, ".env"), []byte("PROPAGATE_DATABASE_URL=postgres://dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadForWorkdir(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PROPAGATE_DATABASE_URL"); got != "postgres://exported" {
		t.Fatalf("PROPAGATE_DATABASE_URL = %q", got)
	}
}
