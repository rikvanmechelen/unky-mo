package tui

import "testing"

// decideTicketStrategy is the pure core of the start-working flow. These
// tests lock in the behavior around the two bug cases the ticket flow
// previously got wrong:
//
//  1. No session, dirty main — used to dead-end with a "press M" hint that
//     went nowhere; now falls through to creating a worktree instead of
//     trying to churn main's dirty checkout.
//  2. Project mapped but missing on disk — must bail out early rather than
//     letting git operations fail with a confusing error.
func TestDecideTicketStrategy(t *testing.T) {
	tests := []struct {
		name                 string
		projectOnDisk        bool
		hasSession           bool
		dirty                bool
		existingWorktreePath string
		want                 ticketStartStrategy
	}{
		{
			name:          "missing project wins over everything",
			projectOnDisk: false,
			hasSession:    true,
			dirty:         true,
			want:          strategyMissing,
		},
		{
			name:                 "existing worktree focus even with session",
			projectOnDisk:        true,
			hasSession:           true,
			existingWorktreePath: "/tmp/foo.worktrees/feat",
			want:                 strategyFocusExisting,
		},
		{
			name:                 "existing worktree focus even when dirty",
			projectOnDisk:        true,
			dirty:                true,
			existingWorktreePath: "/tmp/foo.worktrees/feat",
			want:                 strategyFocusExisting,
		},
		{
			name:          "session running → worktree",
			projectOnDisk: true,
			hasSession:    true,
			want:          strategyWorktree,
		},
		{
			name:          "dirty main, no session → worktree (regression: was dead-ending)",
			projectOnDisk: true,
			dirty:         true,
			want:          strategyWorktree,
		},
		{
			name:          "clean main, no session → main checkout (happy path)",
			projectOnDisk: true,
			want:          strategyMainCheckout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideTicketStrategy(tc.projectOnDisk, tc.hasSession, tc.dirty, tc.existingWorktreePath)
			if got != tc.want {
				t.Errorf("decideTicketStrategy(onDisk=%v, session=%v, dirty=%v, wt=%q) = %q, want %q",
					tc.projectOnDisk, tc.hasSession, tc.dirty, tc.existingWorktreePath, got, tc.want)
			}
		})
	}
}
