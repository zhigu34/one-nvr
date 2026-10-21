package main

import "testing"

// The guard is the whole safety story for a command that deletes every recording
// on a site: it must refuse to run without the exact confirmation argument, and
// must accept the dry run without it.
func TestPurgeHistoryRefusesWithoutConfirmation(t *testing.T) {
	for _, args := range [][]string{
		{"purge-history"},
		{"purge-history", "--verbose"},
		{"purge-history", "--confirm"},
		{"purge-history", "--confirm-delete-histories"},
	} {
		handled, err := purgeHistoryCommand(args)
		if !handled {
			t.Fatalf("%v was not recognised as the purge command", args)
		}
		if err == nil {
			t.Fatalf("%v was accepted without the exact confirmation argument", args)
		}
	}
}

// A dry run is the way an operator checks the inventory first, so it must not
// require the destructive confirmation.
func TestPurgeHistoryDryRunNeedsNoConfirmation(t *testing.T) {
	handled, err := purgeHistoryCommand([]string{"purge-history", "--dry-run"})
	if !handled {
		t.Fatal("--dry-run was not recognised")
	}
	// It will fail later on config.Load in a unit test, which is fine: what this
	// asserts is that it got past the confirmation gate.
	if err != nil && err.Error() == "refusing to delete all history" {
		t.Fatal("a dry run was refused for lacking the confirmation argument")
	}
}
