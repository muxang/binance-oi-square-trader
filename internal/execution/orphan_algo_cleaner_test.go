// Round R.3 orphan_algo_cleaner unit tests — focus on isOrphan decision matrix.
// Audit log INSERT is exercised in deploy verify (real PG); unit tests inject
// nil pgxpool and use the cancellation tracking only.
package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"trader/internal/binance"
)

type fakeOrphanBinance struct {
	algos        []binance.AlgoOpenOrder
	positions    []binance.PositionRisk
	listAlgosErr error
	getPosErr    error
	cancelled    []int64
	cancelErr    map[int64]error
}

func (f *fakeOrphanBinance) ListOpenAlgoOrders(_ context.Context) ([]binance.AlgoOpenOrder, error) {
	return f.algos, f.listAlgosErr
}
func (f *fakeOrphanBinance) GetPositionRisk(_ context.Context, _ string) ([]binance.PositionRisk, error) {
	return f.positions, f.getPosErr
}
func (f *fakeOrphanBinance) CancelAlgoOrder(_ context.Context, _ string, algoID int64) error {
	if e, ok := f.cancelErr[algoID]; ok {
		return e
	}
	f.cancelled = append(f.cancelled, algoID)
	return nil
}

// newTestCleaner wires the cleaner with a nil PG pool — audit writes will fail
// but cancel + metrics + decision logic are exercised. cancelOrphan's audit
// branch logs a warning rather than aborting, so tests focused on decision
// logic stay clean.
func newTestCleaner(bc OrphanAlgoBinance) *OrphanAlgoCleaner {
	return &OrphanAlgoCleaner{bc: bc, db: nil, log: zerolog.Nop()}
}

func algoOpen(id int64, sym, side, typ, status string, reduceOnly, closePos bool) binance.AlgoOpenOrder {
	return binance.AlgoOpenOrder{
		AlgoID: id, Symbol: sym, Side: side, OrderType: typ, Status: status,
		Quantity:      decimal.NewFromFloat(0.1),
		ReduceOnly:    reduceOnly,
		ClosePosition: closePos,
	}
}

func posAt(sym string, amt float64) binance.PositionRisk {
	return binance.PositionRisk{Symbol: sym, PositionAmt: decimal.NewFromFloat(amt)}
}

func TestCleaner_AllAlgosHavePositions_NothingCancelled(t *testing.T) {
	bc := &fakeOrphanBinance{
		algos: []binance.AlgoOpenOrder{
			algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", true, false),
			algoOpen(2, "ETHUSDT", "SELL", "TRAILING_STOP_MARKET", "NEW", true, false),
		},
		positions: []binance.PositionRisk{posAt("BTCUSDT", 0.01), posAt("ETHUSDT", 0.5)},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled, "all algos backed by positions → no cancels")
}

func TestCleaner_PositionClosed_AlgoCancelled(t *testing.T) {
	// BTC algo orphaned (no position), ETH still has position.
	bc := &fakeOrphanBinance{
		algos: []binance.AlgoOpenOrder{
			algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", true, false),
			algoOpen(2, "ETHUSDT", "SELL", "TRAILING_STOP_MARKET", "NEW", true, false),
		},
		positions: []binance.PositionRisk{posAt("ETHUSDT", 0.5)},
	}
	// db=nil → audit insert fails but cancel still succeeds. Cleaner logs warn.
	defer func() { _ = recover() }()
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Equal(t, []int64{1}, bc.cancelled, "BTC algo cancelled (orphan)")
}

func TestCleaner_BuySide_NotCancelled(t *testing.T) {
	// BUY side is entry-bound, not exit; never an orphan candidate.
	bc := &fakeOrphanBinance{
		algos:     []binance.AlgoOpenOrder{algoOpen(1, "BTCUSDT", "BUY", "STOP_MARKET", "NEW", false, false)},
		positions: []binance.PositionRisk{},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled)
}

func TestCleaner_NotReduceOnly_NotCancelled(t *testing.T) {
	// SELL but NOT reduceOnly AND NOT closePosition → could be a leftover from
	// non-exit flow; defensively skip.
	bc := &fakeOrphanBinance{
		algos: []binance.AlgoOpenOrder{
			algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", false, false),
		},
		positions: []binance.PositionRisk{},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled, "not reduceOnly → skip (not our concern)")
}

func TestCleaner_FinishedStatus_NotCancelled(t *testing.T) {
	// Terminal statuses already done — don't touch.
	bc := &fakeOrphanBinance{
		algos: []binance.AlgoOpenOrder{
			algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "FINISHED", true, false),
			algoOpen(2, "ETHUSDT", "SELL", "STOP_MARKET", "CANCELED", true, false),
		},
		positions: []binance.PositionRisk{},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled)
}

func TestCleaner_WorkingStatus_OrphanCancelled(t *testing.T) {
	// WORKING is also active and should be cancelled when orphan.
	bc := &fakeOrphanBinance{
		algos:     []binance.AlgoOpenOrder{algoOpen(1, "BTCUSDT", "SELL", "TRAILING_STOP_MARKET", "WORKING", true, false)},
		positions: []binance.PositionRisk{},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Equal(t, []int64{1}, bc.cancelled, "WORKING orphan also cancelled")
}

func TestCleaner_ClosePositionFlag_QualifiesAsOrphan(t *testing.T) {
	// reduceOnly=false but closePosition=true → still exit-bound; orphan if no position.
	bc := &fakeOrphanBinance{
		algos:     []binance.AlgoOpenOrder{algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", false, true)},
		positions: []binance.PositionRisk{},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Equal(t, []int64{1}, bc.cancelled, "closePosition=true → exit-bound → cancel as orphan")
}

func TestCleaner_ListAlgosFails_TickSkipsCleanup(t *testing.T) {
	bc := &fakeOrphanBinance{listAlgosErr: errors.New("network timeout")}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled)
}

func TestCleaner_GetPositionsFails_TickSkipsCleanup(t *testing.T) {
	// If we can't list positions we can't decide orphans — fail safe.
	bc := &fakeOrphanBinance{
		algos:     []binance.AlgoOpenOrder{algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", true, false)},
		getPosErr: errors.New("rate limited"),
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Empty(t, bc.cancelled, "without position view, don't cancel anything")
}

func TestCleaner_CancelFails_DoesNotBlockOtherOrphans(t *testing.T) {
	bc := &fakeOrphanBinance{
		algos: []binance.AlgoOpenOrder{
			algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", true, false),
			algoOpen(2, "ETHUSDT", "SELL", "STOP_MARKET", "NEW", true, false),
		},
		positions: []binance.PositionRisk{},
		cancelErr: map[int64]error{1: errors.New("binance 429")},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	require.Equal(t, []int64{2}, bc.cancelled, "id=1 failed but id=2 still processed")
}

func TestCleaner_PositionZeroAmt_AlgoCancelled(t *testing.T) {
	// Defensive: V3 already filters zero positions, but if a zero leaks through
	// we still treat as orphan (no position present).
	bc := &fakeOrphanBinance{
		algos:     []binance.AlgoOpenOrder{algoOpen(1, "BTCUSDT", "SELL", "STOP_MARKET", "NEW", true, false)},
		positions: []binance.PositionRisk{posAt("BTCUSDT", 0)},
	}
	c := newTestCleaner(bc)
	c.ReconcileTick(context.Background())
	assert.Equal(t, []int64{1}, bc.cancelled, "positionAmt=0 → orphan")
}
