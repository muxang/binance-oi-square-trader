// ============================================================================
// 用户提供的参考实现 — Binance OI / 大户持仓监控
// ============================================================================
//
// 用途:本项目用 Go 重写,逻辑参考。
// 锚定的业务逻辑(必须 1:1 还原):
//   - checkOpenInterestSurge        → OI 暴涨判定(本项目主信号)
//   - checkLargeHolderRatioWithData → 大户多空比异常(v0.2 备用)
//   - checkMarketCapRatio           → 持仓市值占比(v0.2 备用)
//   - calculateMarketCapRatio       → 市值占比计算
//
// 阈值、回溯周期、判定条件必须与本文件保持一致。
// 详见 references/user-snippets/README.md。
// ============================================================================

import BinanceAPI from "./binanceApi.js";
import FeishuAlert from "./feishuAlert.js";
import { config } from "./config.js";
import { RetryUtils } from "./retryUtils.js";
import { DisplayUtils } from "./displayUtils.js";

class ContractMonitor {
  constructor() {
    this.api = new BinanceAPI();
    this.alerter = new FeishuAlert();
    this.contractCache = new Map();
    this.sampleData = [];
    this.alertsTriggered = 0;
    // 为Web Dashboard存储完整的监控数据
    this.latestMonitoringData = new Map();
  }

  async initialize() {
    console.log("初始化合约监控系统...");
    try {
      const contracts = await this.api.getAllContracts();
      console.log(`获取到 ${contracts.length} 个合约`);
      return contracts;
    } catch (error) {
      console.error("初始化失败:", error.message);
      throw error;
    }
  }

  async monitorContract(symbol) {
    return await RetryUtils.withRetryAndTimeout(
      async () => {
        // 并行获取基础数据、持仓量统计数据、价格数据、流通量和价格趋势
        const [
          accountRatioData,
          positionRatioData,
          openInterestData,
          openInterestStats,
          priceData,
          circulatingSupply,
          priceTrend,
          smartMoneyData,
          spotCapitalFlow,
        ] = await Promise.all([
          this.api.getTopLongShortAccountRatio(symbol).catch(() => []),
          this.api.getTopLongShortPositionRatio(symbol).catch(() => []),
          this.api.getOpenInterest(symbol).catch(() => ({ openInterest: "0" })),
          this.api.getOpenInterestStats(symbol).catch(() => null),
          this.api.get24hrTicker(symbol).catch(() => ({ lastPrice: "0" })),
          this.api.getCirculatingSupply(symbol).catch(() => null),
          this.api.getPriceTrend(symbol).catch(() => null),
          this.api.getSmartMoneySignal(symbol).catch(() => null),
          this.api.getSpotCapitalFlow(symbol, 5).catch(() => null),
        ]);

        // 快速验证数据
        if (accountRatioData.length === 0 || positionRatioData.length === 0) {
          return { symbol, status: "skipped", reason: "数据不足" };
        }

        const accountRatio = parseFloat(accountRatioData[0].longShortRatio);
        const positionRatio = parseFloat(positionRatioData[0].longShortRatio);
        const currentPrice = parseFloat(priceData.lastPrice || "0");
        const currentOpenInterest = parseFloat(
          openInterestData.openInterest || "0"
        );

        // 计算市值占比信息
        const marketCapInfo = this.calculateMarketCapRatio(
          currentOpenInterest,
          currentPrice,
          circulatingSupply
        );

        // 执行大户持仓比例检查
        const holderCheckResult = await this.checkLargeHolderRatioWithData(
          symbol,
          accountRatio,
          positionRatio,
          currentPrice,
          marketCapInfo,
          priceData,
          currentOpenInterest,
          priceTrend
        );

        // 执行持仓量暴涨检查
        const surgeCheckResult = await this.checkOpenInterestSurge(
          symbol,
          openInterestStats,
          currentPrice,
          marketCapInfo,
          priceData,
          priceTrend
        );

        // 执行市值占比检查（50%阈值）
        const marketCapCheckResult = await this.checkMarketCapRatio(
          symbol,
          currentPrice,
          marketCapInfo,
          priceData,
          currentOpenInterest,
          circulatingSupply,
          priceTrend
        );

        // 存储完整的监控数据供Web Dashboard使用
        this.latestMonitoringData.set(symbol, {
          symbol,
          timestamp: Date.now(),
          accountRatio,
          positionRatio,
          currentPrice,
          currentOpenInterest,
          marketCapInfo,
          circulatingSupply,
          priceData,
          priceTrend,
          smartMoneyData,
          spotCapitalFlow,
          threshold: config.monitoring.largeHolderThreshold
        });

        return {
          symbol,
          status: "completed",
          holderCheck: !!holderCheckResult,
          surgeCheck: !!surgeCheckResult,
          marketCapCheck: !!marketCapCheckResult,
        };
      },
      { maxRetries: 1, baseDelay: 500 },
      15000 // 15秒总超时
    ).catch((error) => {
      console.error(`监控合约 ${symbol} 失败:`, error.message);
      return { symbol, status: "failed", error: error.message };
    });
  }

  calculateMarketCapRatio(openInterest, price, circulatingSupply) {
    const marketCapInfo = {
      openInterestValue: openInterest * price, // 持仓市值
      circulatingMarketCap: null, // 流通市值
      ratio: null, // 占比
      hasValidData: false,
    };

    if (circulatingSupply && circulatingSupply > 0 && price > 0) {
      marketCapInfo.circulatingMarketCap = circulatingSupply * price;
      marketCapInfo.ratio =
        (marketCapInfo.openInterestValue / marketCapInfo.circulatingMarketCap) *
        100;
      marketCapInfo.hasValidData = true;
    }

    return marketCapInfo;
  }

  // ==========================================================================
  // ⭐ 本项目核心:OI 暴涨判定
  // ==========================================================================
  async checkOpenInterestSurge(
    symbol,
    openInterestStats,
    currentPrice = 0,
    marketCapInfo = null,
    priceData = null,
    priceTrend = null
  ) {
    if (
      !openInterestStats ||
      !openInterestStats.openInterestData ||
      openInterestStats.openInterestData.length < 6
    ) {
      return null;
    }

    const data = openInterestStats.openInterestData;
    const currentValue = data[data.length - 1];

    // 找到最近数据中的最低值（最多回溯10个周期）
    const lookbackPeriods = Math.min(10, data.length);
    let minValue = currentValue;
    let minIndex = data.length - 1;

    for (let i = data.length - lookbackPeriods; i < data.length; i++) {
      if (data[i] < minValue) {
        minValue = data[i];
        minIndex = i;
      }
    }

    // 计算从最低点到当前的增长幅度
    const growthFromMin = (currentValue - minValue) / minValue;

    // 计算最近6个周期的总体趋势（检查是否总体呈增长）
    const recentPeriods = Math.min(6, data.length);
    const recentStartValue = data[data.length - recentPeriods];
    const recentGrowth = (currentValue - recentStartValue) / recentStartValue;

    // 统计最近周期中上涨的次数
    let growingPeriods = 0;
    for (let i = data.length - recentPeriods + 1; i < data.length; i++) {
      const current = data[i];
      const previous = data[i - 1];
      if (current > previous) {
        growingPeriods++;
      }
    }

    // 计算总体增长幅度(最新值相比最早值)
    const overallGrowth = (data[data.length - 1] - data[0]) / data[0];

    // 警报条件：
    // 1. 从最低点增长 >= 10%   (本项目改为 5%, 见 SPEC)
    // 2. 最近6周期总体呈增长趋势(>= 3%)
    // 3. 最近周期中超过一半时间在上涨
    const isAlert =
      growthFromMin >= config.monitoring.minSurgePercentage &&
      recentGrowth >= 0.03 &&
      growingPeriods >= Math.floor(recentPeriods / 2);

    // ... [告警发送逻辑省略, Go 版本走 EventBus → Strategy]
    return {
      symbol,
      surgeData: {
        growingPeriods,
        recentPeriodsCount: recentPeriods - 1,
        recentGrowthPercentage: recentGrowth * 100,
        growthFromMinPercentage: growthFromMin * 100,
        overallGrowthPercentage: overallGrowth * 100,
        totalPeriods: data.length,
        currentOpenInterest: currentValue,
        minOpenInterest: minValue,
        minValueIndex: minIndex,
        isAlert,
      },
    };
  }

  async checkLargeHolderRatioWithData(
    symbol,
    accountRatio,
    positionRatio,
    currentPrice = 0,
    marketCapInfo = null,
    priceData = null,
    currentOpenInterest = 0,
    priceTrend = null
  ) {
    const threshold = config.monitoring.largeHolderThreshold;
    const isAlert = accountRatio < threshold || positionRatio < threshold;

    // ... [告警发送逻辑省略]
    return { symbol, holderData: { accountRatio, positionRatio, threshold, isAlert } };
  }

  async checkMarketCapRatio(
    symbol,
    currentPrice,
    marketCapInfo,
    priceData,
    currentOpenInterest,
    circulatingSupply,
    priceTrend
  ) {
    if (!marketCapInfo || !marketCapInfo.hasValidData) {
      return null;
    }

    const threshold = 50; // 50%阈值
    const isAlert = marketCapInfo.ratio >= threshold;
    // ... [告警发送逻辑省略]
    return { symbol, marketCapData: { ratio: marketCapInfo.ratio, threshold, isAlert } };
  }
}

export default ContractMonitor;
