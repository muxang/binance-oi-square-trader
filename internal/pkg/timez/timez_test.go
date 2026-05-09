package timez

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBJTLocation(t *testing.T) {
	require.NotNil(t, BJT)
	assert.Equal(t, "Asia/Shanghai", BJT.String())
}

func TestNowUTC(t *testing.T) {
	got := NowUTC()
	assert.Equal(t, time.UTC, got.Location())
	diff := time.Since(got)
	if diff < 0 {
		diff = -diff
	}
	assert.Less(t, diff, time.Second, "NowUTC drifted too far from time.Now")
}

func TestTodayStartBJT(t *testing.T) {
	// All four cases land in the same BJT calendar day (2026-05-09 BJT),
	// so they must all map to the same UTC moment for that day's BJT 00:00.
	expected := time.Date(2026, 5, 8, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		now  time.Time
	}{
		{"BJT 08:00:00", time.Date(2026, 5, 9, 8, 0, 0, 0, BJT)},
		{"BJT 00:00:00", time.Date(2026, 5, 9, 0, 0, 0, 0, BJT)},
		{"BJT 23:59:59", time.Date(2026, 5, 9, 23, 59, 59, 0, BJT)},
		{"UTC 01:00:00 (= BJT 09:00)", time.Date(2026, 5, 9, 1, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TodayStartBJT(c.now)
			assert.True(t, got.Equal(expected), "expected %v got %v", expected, got)
			assert.Equal(t, time.UTC, got.Location())
		})
	}
}

func TestFormatBJT(t *testing.T) {
	// 2026-05-09 00:00:00 UTC = 2026-05-09 08:00:00 BJT
	moment := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	got := FormatBJT(moment, time.RFC3339)
	assert.Equal(t, "2026-05-09T08:00:00+08:00", got)
}
