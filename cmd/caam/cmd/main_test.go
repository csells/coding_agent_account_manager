package cmd

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestMain runs this package's tests under an isolated HOME so nothing they
// exercise can reach the developer's real auth files. See testutil.IsolatedMain.
//
// It also fences spawnExec: a test that reaches the real syscall.Exec replaces
// the test process with whatever is first on PATH (under IsolatedMain, a no-op
// stand-in that exits 0), which silently ends the suite there and reports it
// green. Tests that want to observe the exec inject their own fake and restore
// this one.
func TestMain(m *testing.M) {
	spawnExec = func(binPath string, args []string, _ []string) error {
		return fmt.Errorf("spawnExec fenced in tests: would exec %s %v", binPath, args)
	}
	os.Exit(testutil.IsolatedMain(m))
}
