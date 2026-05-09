# ============================================================================
# 用户提供的参考实现 — Binance Square Hashtag 查询
# ============================================================================
#
# 用途:本项目用 Go 重写,逻辑参考。
# 锚定的业务逻辑:
#   - URL、参数、headers、字段路径必须 1:1 一致
#   - 失败时返回 (0, 0),不抛异常
#
# 详见 references/user-snippets/README.md
# ============================================================================

import requests


def get_square_discussion(coin):
    try:
        r = requests.get(
            "https://www.binance.com/bapi/composite/v4/friendly/pgc/content/queryByHashtag",
            params={
                "hashtag": f"#{coin.lower()}",
                "pageIndex": 1,
                "pageSize": 1,
                "orderBy": "HOT"
            },
            headers={
                "User-Agent": "Mozilla/5.0",
                "Referer": "https://www.binance.com/en/square"
            },
            timeout=8
        )
        if r.status_code == 200:
            ht = r.json().get("data", {}).get("hashtag", {})
            return ht.get("contentCount", 0), ht.get("viewCount", 0)
    except Exception:
        pass
    return 0, 0


# 用法:
# count, views = get_square_discussion("btc")
# 返回: (contentCount, viewCount), 失败时 (0, 0)
