package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	usageURL   = "https://api.anthropic.com/api/oauth/usage"
	betaHeader = "oauth-2025-04-20"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// apiResponse mirrors the subset of /api/oauth/usage fields we care about.
// Extra fields (monthly extra_usage, per-model weekly splits other than
// opus/sonnet) are ignored by encoding/json.
type apiResponse struct {
	FiveHour       apiWindow `json:"five_hour"`
	SevenDay       apiWindow `json:"seven_day"`
	SevenDayOpus   apiWindow `json:"seven_day_opus"`
	SevenDaySonnet apiWindow `json:"seven_day_sonnet"`
}

type apiWindow struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// httpStatus is distinct from (apiResponse, error) so callers (fetchWithDeps)
// can react to 429/401 without string-matching error messages.
type httpStatus struct {
	code int
	body apiResponse
	err  error
}

type apiClient interface {
	call(ctx context.Context, token string) httpStatus
}

type realClient struct{}

func (realClient) call(ctx context.Context, token string) httpStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return httpStatus{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return httpStatus{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return httpStatus{code: resp.StatusCode, err: ErrAuthExpired}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return httpStatus{code: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatus{code: resp.StatusCode, err: fmt.Errorf("usage: unexpected status %d", resp.StatusCode)}
	}

	var body apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return httpStatus{code: resp.StatusCode, err: fmt.Errorf("usage: decode: %w", err)}
	}
	return httpStatus{code: resp.StatusCode, body: body}
}

func toSnapshot(r apiResponse, fetchedAt time.Time, source string) Snapshot {
	return Snapshot{
		FiveHour:       Window{Utilization: r.FiveHour.Utilization, ResetsAt: r.FiveHour.ResetsAt},
		SevenDay:       Window{Utilization: r.SevenDay.Utilization, ResetsAt: r.SevenDay.ResetsAt},
		SevenDayOpus:   Window{Utilization: r.SevenDayOpus.Utilization, ResetsAt: r.SevenDayOpus.ResetsAt},
		SevenDaySonnet: Window{Utilization: r.SevenDaySonnet.Utilization, ResetsAt: r.SevenDaySonnet.ResetsAt},
		FetchedAt:      fetchedAt,
		Source:         source,
	}
}

func fromSnapshot(s Snapshot) apiResponse {
	return apiResponse{
		FiveHour:       apiWindow{Utilization: s.FiveHour.Utilization, ResetsAt: s.FiveHour.ResetsAt},
		SevenDay:       apiWindow{Utilization: s.SevenDay.Utilization, ResetsAt: s.SevenDay.ResetsAt},
		SevenDayOpus:   apiWindow{Utilization: s.SevenDayOpus.Utilization, ResetsAt: s.SevenDayOpus.ResetsAt},
		SevenDaySonnet: apiWindow{Utilization: s.SevenDaySonnet.Utilization, ResetsAt: s.SevenDaySonnet.ResetsAt},
	}
}
