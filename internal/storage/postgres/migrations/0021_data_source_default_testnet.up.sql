-- R.17 D2: data_source 列 DEFAULT 'mainnet' 是 migration 0008 的遗留 bug。
-- trader 自始至终运行 TRADER_MODE=testnet (启动 banner 确认),
-- 但因 trader INSERT 不显式传 data_source,所有 187 笔 trade 错标为 'mainnet'。
--
-- 此 migration:
-- 1. 把 DEFAULT 改为 'testnet' (当前真实模式)
-- 2. backfill 历史:entry_ts > '2026-05-12' (mode 切换后) 全部改为 'testnet'
--
-- 切 mainnet 时:必须先跑一个新 migration 把 DEFAULT 改回 'mainnet',
-- 否则真实主网下单又会被错标 testnet。这是 A5 切换 checklist 一项。

ALTER TABLE trades      ALTER COLUMN data_source SET DEFAULT 'testnet';
ALTER TABLE trade_exits ALTER COLUMN data_source SET DEFAULT 'testnet';

-- Backfill: 在 2026-05-12(trader 启动 testnet 模式)之后的所有 trade。
-- 早于这个日期的少数行可能是真 mainnet 测试残留,保守不动。
UPDATE trades      SET data_source = 'testnet' WHERE entry_ts > '2026-05-12 00:00:00+00' AND data_source = 'mainnet';
UPDATE trade_exits SET data_source = 'testnet' WHERE ts        > '2026-05-12 00:00:00+00' AND data_source = 'mainnet';
