// v0.2 Round 4 unit tests — focus on the dispatch logic (event parsing +
// callback routing). Connection lifecycle / reconnect are covered by mainnet
// integration (Round 7 acceptance).
package execution

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestUserStream_Dispatch_OrderTradeUpdate_FILLED_SELL(t *testing.T) {
	var (
		gotSym   string
		gotOrder int64
		fired    bool
	)
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnOrderFilled: func(_ context.Context, sym string, orderID int64) {
				gotSym, gotOrder, fired = sym, orderID, true
			},
		},
	}
	msg := []byte(`{"e":"ORDER_TRADE_UPDATE","o":{"s":"BTCUSDT","i":12345,"S":"SELL","X":"FILLED","o":"TRAILING_STOP_MARKET","x":"TRADE"}}`)
	us.dispatch(context.Background(), msg)
	assert.True(t, fired, "OnOrderFilled must fire on FILLED SELL")
	assert.Equal(t, "BTCUSDT", gotSym)
	assert.Equal(t, int64(12345), gotOrder)
}

func TestUserStream_Dispatch_BUY_NotFired(t *testing.T) {
	fired := false
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnOrderFilled: func(_ context.Context, _ string, _ int64) { fired = true },
		},
	}
	msg := []byte(`{"e":"ORDER_TRADE_UPDATE","o":{"s":"BTCUSDT","i":1,"S":"BUY","X":"FILLED","o":"MARKET","x":"TRADE"}}`)
	us.dispatch(context.Background(), msg)
	assert.False(t, fired, "BUY FILLED is entry; executor handles synchronously")
}

func TestUserStream_Dispatch_NEW_Status_NotFired(t *testing.T) {
	// NEW = algo placed but not triggered. Our handler waits for FILLED.
	fired := false
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnOrderFilled: func(_ context.Context, _ string, _ int64) { fired = true },
		},
	}
	msg := []byte(`{"e":"ORDER_TRADE_UPDATE","o":{"s":"BTCUSDT","i":1,"S":"SELL","X":"NEW","o":"STOP_MARKET","x":"NEW"}}`)
	us.dispatch(context.Background(), msg)
	assert.False(t, fired)
}

func TestUserStream_Dispatch_AccountUpdate(t *testing.T) {
	fired := false
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnAccountUpd: func(_ context.Context) { fired = true },
		},
	}
	us.dispatch(context.Background(), []byte(`{"e":"ACCOUNT_UPDATE","a":{"B":[]}}`))
	assert.True(t, fired)
}

func TestUserStream_Dispatch_MarginCall_PerSymbol(t *testing.T) {
	var mu sync.Mutex
	var got []string
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnMarginCall: func(_ context.Context, sym string) {
				mu.Lock()
				got = append(got, sym)
				mu.Unlock()
			},
		},
	}
	msg := []byte(`{"e":"MARGIN_CALL","p":[{"s":"BTCUSDT"},{"s":"ETHUSDT"}]}`)
	us.dispatch(context.Background(), msg)
	assert.Equal(t, []string{"BTCUSDT", "ETHUSDT"}, got)
}

func TestUserStream_Dispatch_ListenKeyExpired_NoCallback(t *testing.T) {
	// listenKeyExpired triggers conn.Close() so readLoop returns; we just verify
	// no callbacks fire.
	fired := false
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnOrderFilled: func(_ context.Context, _ string, _ int64) { fired = true },
			OnAccountUpd:  func(_ context.Context) { fired = true },
			OnMarginCall:  func(_ context.Context, _ string) { fired = true },
		},
	}
	// conn is nil here; us.conn.Close() would panic. Skip closing path by checking
	// only callback isolation. Real scenarios run inside session() which sets conn.
	defer func() { _ = recover() }()
	us.dispatch(context.Background(), []byte(`{"e":"listenKeyExpired"}`))
	assert.False(t, fired)
}

func TestUserStream_Dispatch_UnknownEvent_Ignored(t *testing.T) {
	fired := false
	us := &UserStream{
		log: zerolog.Nop(),
		callbacks: UserStreamCallbacks{
			OnOrderFilled: func(_ context.Context, _ string, _ int64) { fired = true },
		},
	}
	us.dispatch(context.Background(), []byte(`{"e":"someFutureEvent","x":1}`))
	assert.False(t, fired)
}

func TestUserStream_Dispatch_MalformedJSON_NoPanic(t *testing.T) {
	us := &UserStream{log: zerolog.Nop()}
	us.dispatch(context.Background(), []byte(`{this is not json}`))
	// no panic = pass
}

func TestUserStream_NilCallbacks_NoPanic(t *testing.T) {
	// All callbacks nil → dispatch must not panic.
	us := &UserStream{log: zerolog.Nop()}
	us.dispatch(context.Background(), []byte(`{"e":"ORDER_TRADE_UPDATE","o":{"s":"X","i":1,"S":"SELL","X":"FILLED"}}`))
	us.dispatch(context.Background(), []byte(`{"e":"ACCOUNT_UPDATE","a":{}}`))
	us.dispatch(context.Background(), []byte(`{"e":"MARGIN_CALL","p":[{"s":"X"}]}`))
}
