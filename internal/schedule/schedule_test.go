package schedule

import (
	"testing"
	"time"
)

func TestHybridParser_Parse(t *testing.T) {
	parser := NewHybridParser()

	tests := []struct {
		name         string
		input        string
		wantKind     Kind
		wantCron     string
		wantInterval time.Duration
	}{
		// Reboot aliases
		{name: "@reboot", input: "@reboot", wantKind: KindReboot},
		{name: "on reboot", input: "on reboot", wantKind: KindReboot},
		{name: "ON BOOT uppercase", input: "  ON   BOOT ", wantKind: KindReboot},
		{name: "at startup", input: "at startup", wantKind: KindReboot},
		{name: "startup", input: "startup", wantKind: KindReboot},

		// Cron descriptors
		{name: "@hourly", input: "@hourly", wantKind: KindCron, wantCron: "0 * * * *"},
		{name: "@daily", input: "@daily", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "@midnight", input: "@midnight", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "@weekly", input: "@weekly", wantKind: KindCron, wantCron: "0 0 * * 0"},
		{name: "@monthly", input: "@monthly", wantKind: KindCron, wantCron: "0 0 1 * *"},
		{name: "@yearly", input: "@yearly", wantKind: KindCron, wantCron: "0 0 1 1 *"},
		{name: "@annually", input: "@annually", wantKind: KindCron, wantCron: "0 0 1 1 *"},
		{name: "@every 5m", input: "@every 5m", wantKind: KindInterval, wantInterval: 5 * time.Minute},

		// Standard 5-field cron
		{name: "cron every 15m", input: "*/15 * * * *", wantKind: KindCron, wantCron: "*/15 * * * *"},
		{name: "cron weekdays 9am", input: "0 9 * * 1-5", wantKind: KindCron, wantCron: "0 9 * * 1-5"},
		{name: "cron complex", input: "5,35 8-18 * * MON-FRI", wantKind: KindCron, wantCron: "5,35 8-18 * * MON-FRI"},

		// Single words
		{name: "hourly", input: "hourly", wantKind: KindCron, wantCron: "0 * * * *"},
		{name: "daily", input: "daily", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "weekly", input: "weekly", wantKind: KindCron, wantCron: "0 0 * * 0"},
		{name: "monthly", input: "monthly", wantKind: KindCron, wantCron: "0 0 1 * *"},

		// Every intervals
		{name: "every second", input: "every second", wantKind: KindInterval, wantInterval: time.Second},
		{name: "every 30s", input: "every 30s", wantKind: KindInterval, wantInterval: 30 * time.Second},
		{name: "every 45 seconds", input: "every 45 seconds", wantKind: KindInterval, wantInterval: 45 * time.Second},
		{name: "every minute", input: "every minute", wantKind: KindCron, wantCron: "* * * * *"},
		{name: "every 5 minutes", input: "every 5 minutes", wantKind: KindCron, wantCron: "*/5 * * * *"},
		{name: "every 15 mins", input: "every 15 mins", wantKind: KindCron, wantCron: "*/15 * * * *"},
		{name: "every 7 minutes (non-divisor)", input: "every 7 minutes", wantKind: KindInterval, wantInterval: 7 * time.Minute},
		{name: "every hour", input: "every hour", wantKind: KindCron, wantCron: "0 * * * *"},
		{name: "every 2 hours", input: "every 2 hours", wantKind: KindCron, wantCron: "0 */2 * * *"},
		{name: "every 5 hours (non-divisor)", input: "every 5 hours", wantKind: KindInterval, wantInterval: 5 * time.Hour},
		{name: "every 1h30m", input: "every 1h30m", wantKind: KindInterval, wantInterval: 90 * time.Minute},
		{name: "every day", input: "every day", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "every 3 days", input: "every 3 days", wantKind: KindInterval, wantInterval: 72 * time.Hour},
		{name: "every week", input: "every week", wantKind: KindCron, wantCron: "0 0 * * 0"},
		{name: "every 2 weeks", input: "every 2 weeks", wantKind: KindInterval, wantInterval: 14 * 24 * time.Hour},

		// Hourly at minute
		{name: "every hour at :15", input: "every hour at :15", wantKind: KindCron, wantCron: "15 * * * *"},
		{name: "hourly at :30", input: "hourly at :30", wantKind: KindCron, wantCron: "30 * * * *"},
		{name: "every hour at minute 5", input: "every hour at minute 5", wantKind: KindCron, wantCron: "5 * * * *"},

		// Daily / time-only / weekday / weekend
		{name: "at 9am", input: "at 9am", wantKind: KindCron, wantCron: "0 9 * * *"},
		{name: "daily at 9am", input: "daily at 9am", wantKind: KindCron, wantCron: "0 9 * * *"},
		{name: "daily at 12am", input: "daily at 12am", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "daily at 12pm", input: "daily at 12pm", wantKind: KindCron, wantCron: "0 12 * * *"},
		{name: "daily at 12:30pm", input: "daily at 12:30pm", wantKind: KindCron, wantCron: "30 12 * * *"},
		{name: "daily at 14:30", input: "daily at 14:30", wantKind: KindCron, wantCron: "30 14 * * *"},
		{name: "every day at midnight", input: "every day at midnight", wantKind: KindCron, wantCron: "0 0 * * *"},
		{name: "every day at noon", input: "every day at noon", wantKind: KindCron, wantCron: "0 12 * * *"},
		{name: "every weekday at 8:30am", input: "every weekday at 8:30am", wantKind: KindCron, wantCron: "30 8 * * 1-5"},
		{name: "weekdays at 09:00", input: "weekdays at 09:00", wantKind: KindCron, wantCron: "0 9 * * 1-5"},
		{name: "every weekend at 10am", input: "every weekend at 10am", wantKind: KindCron, wantCron: "0 10 * * 0,6"},

		// Specific days of week
		{name: "every monday at 9am", input: "every monday at 9am", wantKind: KindCron, wantCron: "0 9 * * 1"},
		{name: "mondays at 9:15pm", input: "mondays at 9:15pm", wantKind: KindCron, wantCron: "15 21 * * 1"},
		{name: "every mon,wed,fri at 8am", input: "every mon,wed,fri at 8am", wantKind: KindCron, wantCron: "0 8 * * 1,3,5"},
		{name: "every tue and thu at 18:00", input: "every tue and thu at 18:00", wantKind: KindCron, wantCron: "0 18 * * 2,4"},
		{name: "weekly on sun at 2am", input: "weekly on sun at 2am", wantKind: KindCron, wantCron: "0 2 * * 0"},
		{name: "every friday (default midnight)", input: "every friday", wantKind: KindCron, wantCron: "0 0 * * 5"},

		// Monthly
		{name: "monthly on the 1st at 00:00", input: "monthly on the 1st at 00:00", wantKind: KindCron, wantCron: "0 0 1 * *"},
		{name: "every month on day 15 at 9am", input: "every month on day 15 at 9am", wantKind: KindCron, wantCron: "0 9 15 * *"},
		{name: "monthly on 2nd at 6:30pm", input: "monthly on 2nd at 6:30pm", wantKind: KindCron, wantCron: "30 18 2 * *"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parser.Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.input, err)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Parse(%q).Kind = %v, want %v", tc.input, got.Kind, tc.wantKind)
			}
			if got.CronExpr != tc.wantCron {
				t.Errorf("Parse(%q).CronExpr = %q, want %q", tc.input, got.CronExpr, tc.wantCron)
			}
			if got.Interval != tc.wantInterval {
				t.Errorf("Parse(%q).Interval = %v, want %v", tc.input, got.Interval, tc.wantInterval)
			}
		})
	}
}

func TestHybridParser_Invalid(t *testing.T) {
	parser := NewHybridParser()

	invalid := []string{
		"",
		"   ",
		"every 0 minutes",
		"every -5m",
		"@every -1s",
		"@every bogus",
		"99 * * * *",
		"daily at 25:00",
		"daily at 13am",
		"daily at 0am",
		"daily at 9:60am",
		"every hour at :60",
		"monthly on day 32 at 9am",
		"monthly on day 0 at 9am",
		"every funday at 9am",
		"sometime next tuesday",
	}

	for _, expr := range invalid {
		t.Run(expr, func(t *testing.T) {
			if got, err := parser.Parse(expr); err == nil {
				t.Errorf("Parse(%q) expected error, got %+v", expr, got)
			}
		})
	}
}

func TestSchedule_Next(t *testing.T) {
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	parser := NewHybridParser()

	t.Run("cron next runs", func(t *testing.T) {
		s, err := parser.Parse("every hour")
		if err != nil {
			t.Fatal(err)
		}
		runs, err := s.Next(base, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 3 {
			t.Fatalf("got %d runs, want 3", len(runs))
		}
		want := []time.Time{
			time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 26, 13, 0, 0, 0, time.UTC),
		}
		for i := range want {
			if !runs[i].Equal(want[i]) {
				t.Errorf("run[%d] = %v, want %v", i, runs[i], want[i])
			}
		}
	})

	t.Run("interval next runs", func(t *testing.T) {
		s, err := parser.Parse("every 30s")
		if err != nil {
			t.Fatal(err)
		}
		runs, err := s.Next(base, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 2 || !runs[0].Equal(base.Add(30*time.Second)) || !runs[1].Equal(base.Add(time.Minute)) {
			t.Fatalf("unexpected interval runs: %v", runs)
		}
	})

	t.Run("reboot returns nil", func(t *testing.T) {
		s, err := parser.Parse("on reboot")
		if err != nil {
			t.Fatal(err)
		}
		runs, err := s.Next(base, 3)
		if err != nil {
			t.Fatal(err)
		}
		if runs != nil {
			t.Fatalf("expected nil runs for reboot, got %v", runs)
		}
	})
}
