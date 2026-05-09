package timez

import (
	"fmt"
	"time"
)

// BJT is the application timezone for "daily reset" boundaries (BJT 00:00),
// TG alert rendering, and Dashboard display. Loaded once at init.
var BJT *time.Location

func init() {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(fmt.Sprintf("timez: load Asia/Shanghai failed: %v", err))
	}
	BJT = loc
}

// NowUTC returns the current time in UTC. The only sanctioned replacement for
// time.Now() in business code; a CI guard rejects bare time.Now() elsewhere.
func NowUTC() time.Time { return time.Now().UTC() }

// TodayStartBJT returns the UTC moment corresponding to BJT 00:00 of the
// BJT-local calendar day that contains `now`. Used for daily-reset boundaries.
func TodayStartBJT(now time.Time) time.Time {
	bjt := now.In(BJT)
	return time.Date(bjt.Year(), bjt.Month(), bjt.Day(), 0, 0, 0, 0, BJT).UTC()
}

// FormatBJT renders t in BJT using the given layout. For user-facing output
// only (TG alerts, Dashboard); never use in business comparisons or DB writes.
func FormatBJT(t time.Time, layout string) string {
	return t.In(BJT).Format(layout)
}
