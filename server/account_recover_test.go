package server

// #18: a corrupt accounts file must not brick the daemon - it gets
// preserved as .corrupt-<ts>, the store starts empty, and the event is
// surfaced via recoveryNote (the server logs it loudly).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAccountStoreRecoversFromCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, []byte(`[{"apiKey":"k1"`), 0o600); err != nil { // torn JSON
		t.Fatal(err)
	}

	s, err := NewAccountStore(path)
	if err != nil {
		t.Fatalf("corrupt file must not fail startup: %v", err)
	}
	if s.recoveryNote == "" || !strings.Contains(s.recoveryNote, "corrupt") {
		t.Fatalf("recovery not surfaced: %q", s.recoveryNote)
	}
	if len(s.byKey) != 0 {
		t.Fatalf("store must start empty after recovery, got %d accounts", len(s.byKey))
	}
	// the corrupt original is preserved for manual recovery
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("corrupt file not preserved: %v", matches)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path should be free for the fresh save: %v", err)
	}
}
