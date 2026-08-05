package cron

import (
	"testing"
	"time"
)

func TestParseScheduleEmpty(t *testing.T) {
	next, oneShot, err := ParseSchedule("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !oneShot {
		t.Error("expected one-shot for empty schedule")
	}
	if !next.IsZero() {
		t.Error("expected zero next run for one-shot")
	}
}

func TestParseScheduleOnce(t *testing.T) {
	next, oneShot, err := ParseSchedule("@once", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !oneShot {
		t.Error("expected one-shot for @once")
	}
	if !next.IsZero() {
		t.Error("expected zero next run for @once")
	}
}

func TestParseScheduleEveryDuration(t *testing.T) {
	now := time.Now()

	tests := []struct {
		schedule string
		wantDur  time.Duration
	}{
		{"@every 30m", 30 * time.Minute},
		{"@every 2h", 2 * time.Hour},
		{"@every 1d", 24 * time.Hour},
	}

	for _, tt := range tests {
		next, oneShot, err := ParseSchedule(tt.schedule, now)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", tt.schedule, err)
			continue
		}
		if oneShot {
			t.Errorf("ParseSchedule(%q): unexpected one-shot", tt.schedule)
		}
		got := next.Sub(now).Round(time.Minute)
		if got != tt.wantDur {
			t.Errorf("ParseSchedule(%q): got %v, want %v", tt.schedule, got, tt.wantDur)
		}
	}
}

func TestParseScheduleNamed(t *testing.T) {
	now := time.Date(2026, 5, 29, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		schedule string
		wantNext time.Time
	}{
		{"@hourly", time.Date(2026, 5, 29, 16, 30, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)},
		{"@monthly", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		next, oneShot, err := ParseSchedule(tt.schedule, now)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", tt.schedule, err)
			continue
		}
		if oneShot {
			t.Errorf("ParseSchedule(%q): unexpected one-shot", tt.schedule)
		}
		if !next.Equal(tt.wantNext) {
			t.Errorf("ParseSchedule(%q): got %v, want %v", tt.schedule, next, tt.wantNext)
		}
	}
}

func TestParseScheduleInvalid(t *testing.T) {
	_, _, err := ParseSchedule("invalid", time.Now())
	if err == nil {
		t.Error("expected error for invalid schedule")
	}

	_, _, err = ParseSchedule("@every xyz", time.Now())
	if err == nil {
		t.Error("expected error for invalid @every duration")
	}

	if _, _, err = ParseSchedule("@every 0s", time.Now()); err == nil {
		t.Error("expected error for non-positive @every duration")
	}
}

func TestParseScheduleFiveFields(t *testing.T) {
	from := time.Date(2026, 5, 29, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		expr string
		want time.Time
	}{
		{"5 * * * *", time.Date(2026, 5, 29, 11, 5, 0, 0, time.UTC)},
		{"*/5 9 * * *", time.Date(2026, 5, 30, 9, 0, 0, 0, time.UTC)},
		{"0 9 * * 1", time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)},
		{"0 9 * * 0", time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		next, oneShot, err := ParseSchedule(tt.expr, from)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", tt.expr, err)
			continue
		}
		if oneShot || !next.Equal(tt.want) {
			t.Errorf("ParseSchedule(%q) = %v, oneShot=%v, want %v", tt.expr, next, oneShot, tt.want)
		}
	}
}

func TestParseScheduleRejectsInvalidCronFields(t *testing.T) {
	for _, expr := range []string{"0 99 * * *", "0 9 32 * *", "0 9 * 13 *", "0 9 * * 8"} {
		if _, _, err := ParseSchedule(expr, time.Now()); err == nil {
			t.Errorf("ParseSchedule(%q) accepted invalid field", expr)
		}
	}
}
