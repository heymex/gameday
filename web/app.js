(() => {
  const POLL_MS = 2000;
  const API = "/api/data";

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

  function fmtBps(bps) {
    if (bps == null || Number.isNaN(bps)) return "—";
    const abs = Math.abs(bps);
    if (abs >= 1e9) return (bps / 1e9).toFixed(2) + " Gbps";
    if (abs >= 1e6) return (bps / 1e6).toFixed(2) + " Mbps";
    if (abs >= 1e3) return (bps / 1e3).toFixed(1) + " kbps";
    return bps.toFixed(0) + " bps";
  }

  function fmtTime(ts) {
    return new Date(ts * 1000).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }

  function seriesOpts(label, stroke, width = 2) {
    return {
      label,
      stroke,
      width,
      value: (_u, v) => fmtBps(v),
    };
  }

  function makeOpts(titleHint) {
    return {
      width: 100,
      height: 260,
      cursor: { drag: { x: false, y: false } },
      scales: {
        x: { time: true },
        y: {
          range: (_u, min, max) => {
            const hi = Math.max(max, min * 1.05, 1e6);
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
          size: 64,
          values: (_u, splits) => splits.map((v) => fmtBps(v)),
        },
      ],
      series: [
        {},
        seriesOpts("baseline", "#1f7a4d", 2),
        seriesOpts(titleHint + " total", "#1d4f91", 2),
        seriesOpts("other", "#c45c26", 2.5),
      ],
      legend: { live: true },
    };
  }

  function chartWidth(el) {
    return Math.max(320, el.clientWidth || el.parentElement.clientWidth || 640);
  }

  function ensurePlot(key, hint) {
    const slot = charts[key];
    if (slot.plot) return slot.plot;
    slot.el.innerHTML = "";
    slot.plot = new uPlot(makeOpts(hint), [[], [], [], []], slot.el);
    slot.plot.setSize({ width: chartWidth(slot.el), height: 260 });
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
    const distData = [x, baseline, raw[COL.distTotal], raw[COL.distOther]];
    const dmzData = [x, baseline, raw[COL.dmzTotal], raw[COL.dmzOther]];

    ensurePlot("dist", "dist→core").setData(distData);
    ensurePlot("dmz", "dmz→isp").setData(dmzData);

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
      slot.plot.setSize({ width: chartWidth(slot.el), height: 260 });
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
