// Phase 4 Round 6: account balance read for circuit breaker trip evaluation
// (TripDailyLoss / TripTotalFloatLoss read USDT availableBalance).
//
// ref: references/binance/urls.md §「Futures Account Balance V2」GET /fapi/v2/balance
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api/Futures-Account-Balance-V2
// fetched: 2026-05-11

package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/shopspring/decimal"
)

// AccountBalanceRow is one row from /fapi/v2/balance (one per asset).
// Round 6 only consumes USDT; other rows present but ignored.
type AccountBalanceRow struct {
	Asset            string
	Balance          decimal.Decimal // wallet balance
	CrossWalletBalance decimal.Decimal
	AvailableBalance decimal.Decimal // free margin (Round 6 用这个)
	MaxWithdrawAmount decimal.Decimal
}

type accountBalanceResp struct {
	Asset              string `json:"asset"`
	Balance            string `json:"balance"`
	CrossWalletBalance string `json:"crossWalletBalance"`
	AvailableBalance   string `json:"availableBalance"`
	MaxWithdrawAmount  string `json:"maxWithdrawAmount"`
}

// GetAccountBalance fetches all balances; caller picks asset (typically USDT).
// Account data — routes via DoReadAccount (testnet base in testnet mode).
func (c *Client) GetAccountBalance(ctx context.Context) ([]AccountBalanceRow, error) {
	body, err := c.DoReadAccount(ctx, "/fapi/v2/balance", url.Values{}, 5)
	if err != nil {
		return nil, fmt.Errorf("get account balance: %w", err)
	}
	var raw []accountBalanceResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse account balance: %w", err)
	}
	out := make([]AccountBalanceRow, 0, len(raw))
	for _, r := range raw {
		out = append(out, AccountBalanceRow{
			Asset:              r.Asset,
			Balance:            parseDecimalOrZero(r.Balance),
			CrossWalletBalance: parseDecimalOrZero(r.CrossWalletBalance),
			AvailableBalance:   parseDecimalOrZero(r.AvailableBalance),
			MaxWithdrawAmount:  parseDecimalOrZero(r.MaxWithdrawAmount),
		})
	}
	return out, nil
}

// GetUSDTBalance is a convenience helper — picks the USDT row + returns
// availableBalance (= free margin, what Round 6 uses for halt ratios).
// 0 if USDT row not found.
func (c *Client) GetUSDTBalance(ctx context.Context) (decimal.Decimal, error) {
	rows, err := c.GetAccountBalance(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	for _, r := range rows {
		if r.Asset == "USDT" {
			return r.AvailableBalance, nil
		}
	}
	return decimal.Zero, nil
}
