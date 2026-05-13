// Phase 5.2 Round 4: alert message templates.
// Each template returns (title, body, dedupeKey, level) so callers don't
// need to know cooldown semantics. Deep links point to the admin Web UI
// so mu's mobile flow is one tap from notification to action.

package notify

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const adminBase = "https://trader.letsagent.net/admin"

// Halt is the 🔴 critical alert when circuit_breaker or position_manager trips.
// haltType matches halt_rca.halt_type (e.g. "local_only_orphan", "circuit_breaker_daily_loss").
// haltRcaID > 0 links the deep URL to the RCA detail page.
func Halt(haltType, reason string, haltRcaID int64, contextJSON string) (level Level, dedupeKey, title, body string) {
	level = LevelCritical
	dedupeKey = "halt:" + haltType
	title = "Trader halt 触发"
	link := adminBase
	if haltRcaID > 0 {
		link = fmt.Sprintf("%s/audit?halt_event=%d", adminBase, haltRcaID)
	}
	body = fmt.Sprintf(
		"halt_type: %s\nreason: %s\n\ncontext:\n%s\n\n查看 + ack:\n%s",
		haltType, reason, contextJSON, link,
	)
	return
}

// ManualHalt is fired when mu intentionally halts via admin Web UI.
// Cooldown bucketed by hours so re-issuing same duration won't double-notify.
func ManualHalt(durationHours int, note string) (level Level, dedupeKey, title, body string) {
	level = LevelCritical
	dedupeKey = fmt.Sprintf("manual_halt:%dh", durationHours)
	title = "mu 主动 halt"
	body = fmt.Sprintf(
		"持续: %dh\n备注: %s\n\nDashboard: %s",
		durationHours, note, adminBase,
	)
	return
}

// Entry is the 🟢 info alert when a real-money entry fills.
func Entry(symbol string, qty, entryPrice, notional decimal.Decimal, tradeID int64) (level Level, dedupeKey, title, body string) {
	level = LevelInfo
	dedupeKey = fmt.Sprintf("entry:%d", tradeID)
	title = "入场 " + symbol
	body = fmt.Sprintf(
		"symbol: %s\nqty: %s\nentry: %s\nnotional: %s USDT\n\n详情:\n%s/trade/%d",
		symbol, qty.String(), entryPrice.String(), notional.String(), adminBase, tradeID,
	)
	return
}

// EntryFailed is 🟡 warning when an entry attempt failed mid-flight
// (set_margin / set_leverage / place_order / fill_timeout / disaster_stop).
func EntryFailed(symbol, reason string, tradeID int64) (level Level, dedupeKey, title, body string) {
	level = LevelWarning
	dedupeKey = fmt.Sprintf("entry_failed:%s", symbol)
	title = "入场失败 " + symbol
	body = fmt.Sprintf(
		"symbol: %s\nreason: %s\ntrade_id: %d\n\n详情:\n%s/trade/%d",
		symbol, reason, tradeID, adminBase, tradeID,
	)
	return
}

// DailyReport is fired by a daily cron at BJT 00:00.
func DailyReport(dailyPnl, balance decimal.Decimal, openPositions, totalTrades, winCount int) (level Level, dedupeKey, title, body string) {
	level = LevelDaily
	dedupeKey = "daily_report:" + time.Now().Format("2006-01-02")
	title = "Trader 日报 " + time.Now().Format("2006-01-02")
	winRate := decimal.Zero
	if totalTrades > 0 {
		winRate = decimal.NewFromInt(int64(winCount)).Div(decimal.NewFromInt(int64(totalTrades))).Mul(decimal.NewFromInt(100))
	}
	body = fmt.Sprintf(
		"账户余额: %s USDT\n今日 PnL: %s USDT\n当前持仓: %d 笔\n累计交易: %d 笔 (胜 %d, 胜率 %s%%)\n\nDashboard: %s",
		balance.StringFixed(2), dailyPnl.StringFixed(2), openPositions, totalTrades, winCount, winRate.StringFixed(1), adminBase,
	)
	return
}
