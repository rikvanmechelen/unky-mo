package tickets

import (
	"context"
	"sync"
)

// FetchResult bundles the outcome of a single provider call. Err is non-nil
// when the fetch failed; otherwise Tickets holds the normalized rows.
type FetchResult struct {
	Provider string
	Tickets  []Ticket
	Err      error
}

// FetchAll calls every provider in parallel and returns a single combined
// slice of tickets plus per-provider results (so the UI can show errors
// inline without hiding healthy providers).
func FetchAll(ctx context.Context, providers []Provider) ([]Ticket, []FetchResult) {
	if len(providers) == 0 {
		return nil, nil
	}
	results := make([]FetchResult, len(providers))
	var wg sync.WaitGroup
	wg.Add(len(providers))
	for i, p := range providers {
		go func(i int, p Provider) {
			defer wg.Done()
			out, err := p.MyTickets(ctx)
			results[i] = FetchResult{Provider: p.Name(), Tickets: out, Err: err}
		}(i, p)
	}
	wg.Wait()
	var all []Ticket
	for _, r := range results {
		all = append(all, r.Tickets...)
	}
	return all, results
}
