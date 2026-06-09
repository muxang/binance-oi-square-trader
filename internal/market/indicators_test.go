package market

import (
	"errors"
	"math"
	"testing"
)

const epsilon = 1e-6

func almostEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

func TestSMA(t *testing.T) {
	t.Run("full window", func(t *testing.T) {
		got, err := SMA([]float64{1, 2, 3, 4, 5}, 5)
		if err != nil || got != 3 {
			t.Fatalf("got %v err %v, want 3", got, err)
		}
	})
	t.Run("trailing window", func(t *testing.T) {
		got, _ := SMA([]float64{10, 20, 30, 40, 50}, 3)
		if got != 40 {
			t.Errorf("got %v, want 40", got)
		}
	})
	t.Run("insufficient", func(t *testing.T) {
		_, err := SMA([]float64{1, 2}, 5)
		if !errors.Is(err, ErrInsufficientData) {
			t.Errorf("got %v, want ErrInsufficientData", err)
		}
	})
}

func TestEMA(t *testing.T) {
	t.Run("constant series", func(t *testing.T) {
		got, _ := EMA([]float64{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}, 5)
		if !almostEqual(got, 10, epsilon) {
			t.Errorf("got %v, want 10", got)
		}
	})
	// closes 1..10, EMA(5): seed = SMA(1..5)=3, alpha=2/6=0.3333
	// step6: 6*α+3*(1-α)=4; step7=5; step8=6; step9=7; step10=8
	t.Run("hand-checked", func(t *testing.T) {
		got, _ := EMA([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 5)
		if !almostEqual(got, 8.0, 1e-4) {
			t.Errorf("got %v, want 8.0", got)
		}
	})
}

func TestRMA(t *testing.T) {
	got, _ := RMA([]float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5}, 4)
	if !almostEqual(got, 5, epsilon) {
		t.Errorf("got %v, want 5", got)
	}
}

func TestRSI(t *testing.T) {
	t.Run("monotone up", func(t *testing.T) {
		closes := make([]float64, 30)
		for i := range closes {
			closes[i] = float64(i + 1)
		}
		got, _ := RSI(closes, 14)
		if got != 100 {
			t.Errorf("strictly up → RSI=100, got %v", got)
		}
	})
	t.Run("monotone down", func(t *testing.T) {
		closes := make([]float64, 30)
		for i := range closes {
			closes[i] = float64(30 - i)
		}
		got, _ := RSI(closes, 14)
		if got != 0 {
			t.Errorf("strictly down → RSI=0, got %v", got)
		}
	})
	t.Run("flat", func(t *testing.T) {
		closes := make([]float64, 30)
		for i := range closes {
			closes[i] = 100
		}
		got, _ := RSI(closes, 14)
		if got != 50 {
			t.Errorf("flat → RSI=50, got %v", got)
		}
	})
	// Wilder textbook sample, expected RSI(14) ≈ 70.4636 at index 14
	// Source: Wilder 1978, replicated by every TA library (TradingView/Pine).
	t.Run("wilder textbook", func(t *testing.T) {
		closes := []float64{
			44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42,
			45.84, 46.08, 45.89, 46.03, 45.61, 46.28, 46.28,
		}
		got, _ := RSI(closes, 14)
		if !almostEqual(got, 70.4636, 0.01) {
			t.Errorf("Wilder sample: got %v, want ~70.4636", got)
		}
	})
}

func TestADX(t *testing.T) {
	t.Run("strong uptrend", func(t *testing.T) {
		n := 50
		highs := make([]float64, n)
		lows := make([]float64, n)
		closes := make([]float64, n)
		for i := 0; i < n; i++ {
			closes[i] = 100 + float64(i)
			highs[i] = closes[i] + 0.5
			lows[i] = closes[i] - 0.5
		}
		got, _ := ADX(highs, lows, closes, 14)
		if got <= 20 {
			t.Errorf("uptrend ADX should be > 20, got %v", got)
		}
	})
	t.Run("chop", func(t *testing.T) {
		n := 60
		highs := make([]float64, n)
		lows := make([]float64, n)
		closes := make([]float64, n)
		for i := 0; i < n; i++ {
			closes[i] = 100 + 0.5*math.Sin(float64(i)*0.4)
			highs[i] = closes[i] + 0.1
			lows[i] = closes[i] - 0.1
		}
		got, _ := ADX(highs, lows, closes, 14)
		if got >= 30 {
			t.Errorf("chop ADX should be < 30, got %v", got)
		}
	})
	t.Run("insufficient", func(t *testing.T) {
		_, err := ADX(make([]float64, 10), make([]float64, 10), make([]float64, 10), 14)
		if !errors.Is(err, ErrInsufficientData) {
			t.Errorf("want ErrInsufficientData, got %v", err)
		}
	})
	t.Run("length mismatch", func(t *testing.T) {
		_, err := ADX(make([]float64, 50), make([]float64, 49), make([]float64, 50), 14)
		if err == nil {
			t.Error("want length mismatch error")
		}
	})
}

func TestHighest(t *testing.T) {
	got, _ := Highest([]float64{1, 5, 3, 7, 2, 4}, 4)
	if got != 7 {
		t.Errorf("got %v, want 7", got)
	}
}

func TestPctChange(t *testing.T) {
	// 100 → 121 over 2 bars = 21% (1h close vs 2h ago close).
	got, _ := PctChange([]float64{100, 110, 121}, 2)
	if !almostEqual(got, 0.21, 1e-6) {
		t.Errorf("got %v, want 0.21", got)
	}
	got, _ = PctChange([]float64{100, 200}, 1)
	if !almostEqual(got, 1.0, 1e-6) {
		t.Errorf("got %v, want 1.0", got)
	}
	t.Run("zero ref", func(t *testing.T) {
		_, err := PctChange([]float64{0, 1, 2}, 2)
		if err == nil {
			t.Error("zero ref should error")
		}
	})
}
