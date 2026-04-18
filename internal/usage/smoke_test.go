package usage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestRealFetch hits the real /api/oauth/usage endpoint once and prints the
// result. Skipped by default; run with:
//
//	go test -run TestRealFetch -v ./internal/usage/... -tags=manual
//
// …or temporarily set USAGE_SMOKE=1 in the env. Also clears the on-disk
// cache so we always hit the network.
func TestRealFetch(t *testing.T) {
	if os.Getenv("USAGE_SMOKE") == "" {
		t.Skip("set USAGE_SMOKE=1 to exercise the real endpoint")
	}
	_ = os.Remove(cachePath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snap, err := Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	fmt.Printf("source=%s  fetched=%s\n", snap.Source, snap.FetchedAt.Format(time.RFC3339))
	fmt.Printf("5h     util=%.3f  resets=%s  pct=%d%%  in=%s\n",
		snap.FiveHour.Utilization,
		snap.FiveHour.ResetsAt.Format(time.RFC3339),
		PctFromUtil(snap.FiveHour.Utilization),
		FormatResetIn(time.Now(), snap.FiveHour.ResetsAt))
	fmt.Printf("7d     util=%.3f  resets=%s  pct=%d%%\n",
		snap.SevenDay.Utilization,
		snap.SevenDay.ResetsAt.Format(time.RFC3339),
		PctFromUtil(snap.SevenDay.Utilization))
	fmt.Printf("opus   util=%.3f\n", snap.SevenDayOpus.Utilization)
	fmt.Printf("sonnet util=%.3f\n", snap.SevenDaySonnet.Utilization)
	fmt.Printf("bar5h  %s\n", RenderBar(snap.FiveHour.Utilization, 20))
	fmt.Printf("barWk  %s\n", RenderBar(snap.SevenDay.Utilization, 20))
}
