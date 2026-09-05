(() => {
  const POLL_MS = 2000;
  const API = "/api/data";
  const KPI_WINDOW_SEC = 15;

  // API columns: [x, baseline, dist_core, dist_core_other, dmz_isp, dmz_isp_other]
  const COL = {
    x: 0,
    baseline: 1,
    distTotal: 2,
    distOther: 3,
    dmzTotal: 4,
    dmzOther: 5,
  };

  const charts = {
    dist: { el: document.getElementById("chart-dist"), plot: null },
    dmz: { el: document.getElementById("chart-dmz"), plot: null },
  };

  const statusDot = document.getElementById("status-dot");
  const statusText = document.getElementById("status-text");
  const updatedEl = document.getElementById("updated");
  const samplesEl = document.getElementById("samples");
  const kpiBaseline = document.getElementById("kpi-baseline");
  const kpiDistOther = document.getElementById("kpi-dist-other");
  const kpiDmzOther = document.getElementById("kpi-dmz-other");

  function fmtBps(bps) {
    if (bps == null || Number.isNaN(bps)) return "—";
    const abs = Math.abs(bps);
    if (abs >= 1e9) return (bps / 1e9).toFixed(2) + " Gbps";
    if (abs >= 1e6) return (bps / 1e6).toFixed(1) + " Mbps";
    if (abs >= 1e3) return (bps / 1e3).toFixed(0) + " kbps";
    return bps.toFixed(0) + " bps";
  }

  function fmtBpsAxis(bps) {
    if (bps == null || Number.isNaN(bps)) return "";
    const abs = Math.abs(bps);
    if (abs >= 1e9) return (bps / 1e9).toFixed(1) + "G";
    if (abs >= 1e6) return (bps / 1e6).toFixed(0) + "M";
    if (abs >= 1e3) return (bps / 1e3).toFixed(0) + "k";
    return String(Math.round(bps));
  }

  function fmtTime(ts) {
    return new Date(ts * 1000).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }

  function avgTail(col, x, windowSec) {
    if (!col?.length) return null;
    const tEnd = x[x.length - 1];
    let sum = 0;
    let n = 0;
    for (let i = col.length - 1; i >= 0; i--) {
      if (tEnd - x[i] > windowSec) break;
      sum += col[i];
      n++;
    }
    return n ? sum / n : null;
  }

  function seriesOpts(label, stroke, width, fill) {
    const s = {
      label,
      stroke,
      width,
      value: (_u, v) => fmtBps(v),
    };
    if (fill) s.fill = fill;
    return s;
  }

  function makeOpts() {
    return {
      width: 100,
      height: 280,
      cursor: { drag: { x: false, y: false } },
      scales: {
        x: { time: true },
        y: {
          range: (_u, min, max) => {
            const hi = Math.max(max, 5e6);
            return [0, hi * 1.08];
          },
        },
      },
      axes: [
        {
          stroke: "#4d5d6b",
          grid: { stroke: "#d5dee6" },
          ticks: { stroke: "#c5d0da" },
        },
        {
          stroke: "#4d5d6b",
          grid: { stroke: "#d5dee6" },
          ticks: { stroke: "#c5d0da" },
          size: 56,
          values: (_u, splits) => splits.map((v) => fmtBpsAxis(v)),
        },
      ],
      series: [
        {},
        // Emphasize "other" — that's the question this page answers.
        seriesOpts("other (non-broadcast)", "#c45c26", 2.5, "rgba(196, 92, 38, 0.18)"),
        seriesOpts("link total", "#1d4f91", 1.5),
        seriesOpts("broadcast baseline", "#1f7a4d", 1.5),
      ],
      legend: { live: true },
    };
  }

  function chartWidth(el) {
    return Math.max(320, el.clientWidth || el.parentElement.clientWidth || 640);
  }

  function ensurePlot(key) {
    const slot = charts[key];
    if (slot.plot) return slot.plot;
    slot.el.innerHTML = "";
    slot.plot = new uPlot(makeOpts(), [[], [], [], []], slot.el);
    slot.plot.setSize({ width: chartWidth(slot.el), height: 280 });
    return slot.plot;
  }

  function setStatus(ok, detail) {
    statusDot.classList.toggle("live", ok);
    statusDot.classList.toggle("err", !ok);
    statusText.textContent = detail;
  }

  function applyData(raw) {
    if (!Array.isArray(raw) || raw.length < 6 || !raw[COL.x]?.length) {
      setStatus(false, "warming up");
      samplesEl.textContent = "0";
      return;
    }

    const x = raw[COL.x];
    const baseline = raw[COL.baseline];
    // Chart order: other, total, baseline — other first so it draws underneath fills clearly.
    ensurePlot("dist").setData([x, raw[COL.distOther], raw[COL.distTotal], baseline]);
    ensurePlot("dmz").setData([x, raw[COL.dmzOther], raw[COL.dmzTotal], baseline]);

    kpiBaseline.textContent = fmtBps(avgTail(baseline, x, KPI_WINDOW_SEC));
    kpiDistOther.textContent = fmtBps(avgTail(raw[COL.distOther], x, KPI_WINDOW_SEC));
    kpiDmzOther.textContent = fmtBps(avgTail(raw[COL.dmzOther], x, KPI_WINDOW_SEC));

    samplesEl.textContent = String(x.length);
    updatedEl.textContent = fmtTime(x[x.length - 1]);
    setStatus(true, "live");
  }

  async function poll() {
    try {
      const res = await fetch(API, { cache: "no-store" });
      if (!res.ok) throw new Error("HTTP " + res.status);
      applyData(await res.json());
    } catch (err) {
      setStatus(false, "error");
      console.warn("poll failed:", err);
    }
  }

  function resize() {
    for (const slot of Object.values(charts)) {
      if (!slot.plot) continue;
      slot.plot.setSize({ width: chartWidth(slot.el), height: 280 });
    }
  }

  let resizeTimer;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(resize, 100);
  });

  poll();
  setInterval(poll, POLL_MS);
})();
