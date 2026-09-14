package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// caam refresh --all --force would spend every vaulted refresh token in
// one command; it is refused as a usage error before anything is touched.
func TestRefreshAll_RefusesForce(t *testing.T) {
	setupRotatedCodex(t)
	c := &cobra.Command{}
	c.Flags().Bool("all", true, "")
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("force", true, "")
	c.Flags().Bool("quiet", true, "")
	err := runRefresh(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("refresh --all --force should be refused, got %v", err)
	}
}
