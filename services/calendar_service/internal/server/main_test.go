package server_test

import (
	"os"
	"testing"
)

// TestMain stops the database_middleware process that the harness started.
func TestMain(m *testing.M) {
	code := m.Run()
	if shared.cmd != nil && shared.cmd.Process != nil {
		_ = shared.cmd.Process.Kill()
		_, _ = shared.cmd.Process.Wait()
	}
	if shared.dir != "" {
		_ = os.RemoveAll(shared.dir)
	}
	os.Exit(code)
}
