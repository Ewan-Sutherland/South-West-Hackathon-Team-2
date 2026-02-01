import ccxt
import pandas as pd
import numpy as np
from datetime import datetime, timezone, timedelta


class HestonRiskModel:

    def __init__(self, coins, timeframe="1d", days=730, dt=1/252):
        self.coins = coins
        self.timeframe = timeframe
        self.days = days
        self.dt = dt
        self.exchange = ccxt.binance({"enableRateLimit": True})

    # ---------- DATA ----------

    def fetch_prices(self, symbol: str):
        since = int(
            (datetime.now(timezone.utc) - timedelta(days=self.days)).timestamp() * 1000
        )
        ohlcv = self.exchange.fetch_ohlcv(
            symbol,
            timeframe=self.timeframe,
            since=since,
            limit=1000
        )
        prices = pd.DataFrame(
            ohlcv,
            columns=["timestamp", "open", "high", "low", "close", "volume"]
        )
        prices["timestamp"] = pd.to_datetime(prices["timestamp"], unit="ms", utc=True)
        prices.set_index("timestamp", inplace=True)
        return prices[["close"]]

    def get_log_returns_df(self):
        price_dfs = []

        for ticker, symbol in self.coins.items():
            df = self.fetch_prices(symbol)
            df = df.rename(columns={"close": ticker})
            price_dfs.append(df)

        prices = pd.concat(price_dfs, axis=1).dropna()
        log_returns = np.log(prices / prices.shift(1)).dropna()
        log_returns_df = log_returns.T

        return log_returns_df

    # ---------- PARAMETER ESTIMATION ----------

    def estimate_heston_params(self, log_returns_df: pd.DataFrame, row_idx=0):
        r = log_returns_df.iloc[row_idx].dropna()

        # 1) Clip extreme daily returns (winsorise) to stop theta exploding from r^2
        lo, hi = r.quantile(0.01), r.quantile(0.99)
        r = r.clip(lower=lo, upper=hi)

        # 2) Drift
        mu = float(r.mean() / self.dt)

        # 3) Realised variance proxy (annualised) + floor
        v = (r ** 2) / self.dt
        theta = float(max(v.mean(), 1e-10))

        # 4) Mean reversion kappa via regression, with alignment + floor
        dv = v.diff().dropna()
        v_lag = v.shift(1).dropna()

        idx = dv.index.intersection(v_lag.index)
        dv = dv.loc[idx]
        v_lag = v_lag.loc[idx]

        if len(dv) < 2:
            kappa = 1e-6
        else:
            X = (theta - v_lag).values.reshape(-1, 1)
            y = dv.values
            kappa = float(np.linalg.lstsq(X, y, rcond=None)[0][0])
            kappa = max(kappa, 1e-6)

        # 5) Vol of vol from residuals + floor
        residuals = dv.values - kappa * (theta - v_lag).values
        sigma_v = float(np.std(residuals) / np.sqrt(self.dt)) if residuals.size > 0 else 1e-10
        sigma_v = max(sigma_v, 1e-10)

        # 6) Correlation (align indices) + clip
        idx2 = dv.index.intersection(r.index)
        if len(idx2) > 1:
            rho = float(np.corrcoef(r.loc[idx2], dv.loc[idx2])[0, 1])
        else:
            rho = 0.0
        rho = float(np.clip(rho, -0.999, 0.999))

        return {
            "mu": mu,
            "theta": theta,
            "kappa": kappa,
            "sigma_v": sigma_v,
            "rho": rho
        }

    # ---------- SDE ----------

    def simulate_heston(self, params, S0, v0, T=1.0):
        mu = params["mu"]
        theta = params["theta"]
        kappa = params["kappa"]
        sigma_v = params["sigma_v"]
        rho = params["rho"]

        # tiny safety: keep rho numerically valid
        rho = float(np.clip(rho, -0.999, 0.999))

        n_steps = int(T / self.dt)

        S = np.zeros(n_steps + 1)
        v = np.zeros(n_steps + 1)

        S[0] = S0
        v[0] = max(float(v0), 0.0)

        for t in range(n_steps):
            Z1 = np.random.normal()
            Z2 = rho * Z1 + np.sqrt(1 - rho**2) * np.random.normal()

            v_curr = max(v[t], 0.0)  # full truncation-style
            v[t+1] = v_curr + kappa * (theta - v_curr) * self.dt + sigma_v * np.sqrt(v_curr * self.dt) * Z2
            v[t+1] = max(v[t+1], 0.0)

            S[t+1] = S[t] * np.exp(
                (mu - 0.5 * v_curr) * self.dt
                + np.sqrt(v_curr * self.dt) * Z1
            )

        return S, v

    # ---------- MONTE CARLO ----------

    def monte_carlo_vol(self, params, n_sims=500, T=1.0):
        vols = []

        for _ in range(n_sims):
            _, v_path = self.simulate_heston(
                params=params,
                S0=1.0,
                v0=params["theta"],
                T=T
            )
            vols.append(np.sqrt(np.mean(v_path)))

        return float(np.mean(vols))

    # ---------- ALL COINS ----------

    def monte_carlo_vols_all(self, log_returns_df: pd.DataFrame, n_sims=500, T=1.0):
        vols_by_coin = {}
        params_by_coin = {}

        for i, coin in enumerate(log_returns_df.index):
            params = self.estimate_heston_params(log_returns_df, row_idx=i)
            mc_vol = self.monte_carlo_vol(params, n_sims=n_sims, T=T)

            params_by_coin[coin] = params
            vols_by_coin[coin] = mc_vol

        return vols_by_coin, params_by_coin


class RiskParityVolTargetAllocator:
    def __init__(self, target_vol, dt=1/252, annualize=252):
        self.target_vol = target_vol
        self.dt = dt
        self.annualize = annualize

    # 2) Risk parity weights (simple inverse-vol)
    def risk_parity_weights(self, vols_by_coin: dict) -> pd.Series:
        inv = {k: 1.0 / float(v) for k, v in vols_by_coin.items() if float(v) > 0}
        w = pd.Series(inv, dtype=float)
        return w / w.sum()  # 3) normalise

    # Covariance from log returns (needed for portfolio vol in step 4)
    def covariance_annual(self, log_returns_df: pd.DataFrame) -> pd.DataFrame:
        rets = log_returns_df.T  # rows=time, cols=assets
        return rets.cov() * self.annualize

    # 4) Portfolio volatility
    def portfolio_vol(self, weights: pd.Series, cov_annual: pd.DataFrame) -> float:
        w = weights.reindex(cov_annual.index).fillna(0.0).values
        C = cov_annual.values
        return float(np.sqrt(w @ C @ w))

    # 5) Vol targeting (no leverage, leftover goes to cash)
    def vol_target(self, weights: pd.Series, cov_annual: pd.DataFrame):
        vol_before = self.portfolio_vol(weights, cov_annual)
        if vol_before <= 0:
            w_scaled = weights * 0.0
            return w_scaled, 1.0, vol_before, 0.0

        scale = min(self.target_vol / vol_before, 1.0)  # no leverage
        w_scaled = weights * scale
        cash = float(1.0 - w_scaled.sum())
        vol_after = self.portfolio_vol(w_scaled, cov_annual)

        return w_scaled, cash, vol_before, vol_after


COINS = {
    "BTC": "BTC/USDT",
    "ETH": "ETH/USDT",
    "BNB": "BNB/USDT",
    "XRP": "XRP/USDT",
    "SOL": "SOL/USDT",
    "ADA": "ADA/USDT",
    "DOGE": "DOGE/USDT",
    "TRX": "TRX/USDT",
    "LTC": "LTC/USDT",
    "AVAX": "AVAX/USDT",
}

model = HestonRiskModel(COINS)

log_returns_df = model.get_log_returns_df()

vols_by_coin, params_by_coin = model.monte_carlo_vols_all(log_returns_df)

print("\nMonte Carlo Heston volatility estimates:\n")
for coin, vol in vols_by_coin.items():
    print(f"{coin}: {float(vol):.4f}")

allocator = RiskParityVolTargetAllocator(target_vol=0.40)

w_rp = allocator.risk_parity_weights(vols_by_coin)
cov_annual = allocator.covariance_annual(log_returns_df)

w_scaled, cash_w, vol_before, vol_after = allocator.vol_target(
    w_rp, cov_annual
)

print("\nRisk parity weights (inverse-vol):")
for coin, w in w_rp.sort_values(ascending=False).items():
    print(f"{coin}: {w:.4f}")

print(f"\nPortfolio vol before targeting: {vol_before:.4f}")
print(f"Portfolio vol after targeting:  {vol_after:.4f}")
print(f"Cash weight (no leverage):      {cash_w:.4f}")

print("\nFinal weights (incl CASH):")
final = w_scaled.copy()
final["CASH"] = cash_w
print(final.sort_values(ascending=False).round(4))