package ki

import (
	"testing"
	"time"
)

func TestQuotaDuration(t *testing.T) {
	t.Parallel()
	dubai := time.FixedZone("GST", 4*3600)
	// 2026-11-06 14:00 in Dubai (10:00 UTC).
	now := time.Date(2026, 11, 6, 14, 0, 0, 0, dubai)
	tests := []struct {
		name    string
		message string
		want    time.Duration
	}{
		{"not a limit", "tests failed: exit 1", 0},
		{"resets in, compact", "Individual quota reached. Resets in 147h25m10s.", 147*time.Hour + 25*time.Minute + 10*time.Second},
		{"resets in, 1h30m", "RESOURCE_EXHAUSTED (code 429): quota reached. Resets in 1h30m.", 90 * time.Minute},
		{"resets in, spelled out", "You've hit your usage limit. Resets in 2 hours 30 minutes.", 150 * time.Minute},
		{"resets in, spelled out with and", "Usage limit reached, resets in 1 hour and 5 mins", 65 * time.Minute},
		{"reset in, decimals", "quota exhausted, reset in 1.5h", 90 * time.Minute},
		{"12h pm today", "You've hit your limit · resets 7pm", 5 * time.Hour},
		{"12h with at", "Claude usage limit reached. Your limit resets at 7pm.", 5 * time.Hour},
		{"12h with minutes and space", "usage limit reached, resets at 7:30 PM", 5*time.Hour + 30*time.Minute},
		{"12h am rolls to tomorrow", "5-hour limit reached ∙ resets 9am", 19 * time.Hour},
		{"12am is midnight", "usage limit reached, resets 12am", 10 * time.Hour},
		{"12pm is noon, passed, so tomorrow", "usage limit reached, resets 12pm", 22 * time.Hour},
		{"24h clock", "rate limit hit, resets at 19:00", 5 * time.Hour},
		{"24h clock tomorrow", "rate limit hit, resets at 09:15", 19*time.Hour + 15*time.Minute},
		{"IANA zone in parens", "You've hit your limit · resets 7pm (Europe/Berlin)", 8 * time.Hour},
		{"IANA zone bare", "usage limit reached. resets at 19:00 Asia/Kolkata", 3*time.Hour + 30*time.Minute},
		{"UTC", "usage limit reached, resets at 19:00 UTC", 9 * time.Hour},
		{"GMT 12h", "usage limit reached, resets 7pm GMT", 9 * time.Hour},
		{"numeric offset", "usage limit reached, resets at 19:00 +02:00", 7 * time.Hour},
		{"US abbreviation", "usage limit reached, resets at 7pm PST", 17 * time.Hour},
		{"date and time", "Weekly limit reached ∙ resets Nov 9 at 10am", 68 * time.Hour},
		{"date, comma, 24h", "usage limit reached, resets on November 9th, 10:00", 68 * time.Hour},
		{"ISO date", "usage limit reached, resets 2026-11-07 08:00", 18 * time.Hour},
		{"date already past this year is next year", "usage limit reached, resets Nov 5 at 2pm", 365*24*time.Hour - 24*time.Hour},
		{"no reset time", "You've hit your usage limit.", DefaultQuotaCooldown},
		{"429 with no reset time", "HTTP 429 Too Many Requests", DefaultQuotaCooldown},
		{"rate limit with no reset time", "rate limit exceeded, retry later", DefaultQuotaCooldown},
		{"unreadable reset falls back", "usage limit reached, resets soon", DefaultQuotaCooldown},
		{"a bare number is no time", "usage limit reached, resets 5 minutes from now", DefaultQuotaCooldown},
		{"impossible hour falls back", "usage limit reached, resets at 25:00", DefaultQuotaCooldown},
		{"13pm falls back", "usage limit reached, resets at 13pm", DefaultQuotaCooldown},
		{"ISO date in the past falls back", "usage limit reached, resets 2026-11-01 08:00", DefaultQuotaCooldown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := quotaDuration(tt.message, now); got != tt.want {
				t.Fatalf("quotaDuration(%q) = %s, want %s", tt.message, got, tt.want)
			}
		})
	}
}
