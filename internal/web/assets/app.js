(function () {
  "use strict";

  const state = {
    journalFilter: "all",
    selectedOS: "windows",
    hostGuides: [],
    refreshing: false,
  };

  function el(id) {
    return document.getElementById(id);
  }

  function escapeHTML(str) {
    return String(str ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function formatDuration(totalSec) {
    const s = Math.max(0, Math.floor(Number(totalSec) || 0));
    const hrs = Math.floor(s / 3600);
    const mins = Math.floor((s % 3600) / 60);
    const secs = s % 60;
    return [hrs, mins, secs].map((n) => String(n).padStart(2, "0")).join(":");
  }

  function formatTime(iso) {
    if (!iso) return "--";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    return d.toLocaleTimeString([], { hour12: false });
  }

  function initTheme() {
    const saved = localStorage.getItem("tradebox-theme");
    const prefersDark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
    const theme = saved || (prefersDark ? "dark" : "light");
    applyTheme(theme);

    el("theme-toggle").addEventListener("click", function () {
      const current = document.documentElement.getAttribute("data-theme") || "light";
      const next = current === "dark" ? "light" : "dark";
      localStorage.setItem("tradebox-theme", next);
      applyTheme(next);
    });
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    el("theme-toggle").textContent = theme === "dark" ? "Light Mode" : "Dark Mode";
  }

  function statePillMeta(termState) {
    switch (termState) {
      case "running":
        return { label: "RUNNING", cls: "pill-healthy", title: "Running" };
      case "starting":
        return { label: "STARTING", cls: "pill-info", title: "Starting" };
      case "backoff":
        return { label: "RECOVERING", cls: "pill-warn", title: "Recovering" };
      case "circuit_open":
        return { label: "CIRCUIT OPEN", cls: "pill-alert", title: "Circuit Open" };
      default:
        return { label: "STOPPED", cls: "pill-neutral", title: "Stopped" };
    }
  }

  function brokerPillMeta(status) {
    switch (status) {
      case "healthy":
        return { label: "HEALTHY", cls: "pill-healthy" };
      case "degraded":
        return { label: "DEGRADED", cls: "pill-warn" };
      case "down":
        return { label: "DOWN", cls: "pill-alert" };
      default:
        return { label: "PENDING", cls: "pill-neutral" };
    }
  }

  function severityPillMeta(sev) {
    switch (sev) {
      case "error":
      case "critical":
        return { label: sev.toUpperCase(), cls: "pill-alert" };
      case "warn":
        return { label: "WARN", cls: "pill-warn" };
      case "trade":
        return { label: "TRADE", cls: "pill-healthy" };
      default:
        return { label: "INFO", cls: "pill-info" };
    }
  }

  function buildSparklineSVG(history, maxLatencyMs) {
    const width = 170;
    const height = 36;
    const pad = 4;
    if (!Array.isArray(history) || history.length === 0) {
      return '<svg class="sparkline-svg" viewBox="0 0 170 36" aria-hidden="true"><line x1="4" y1="18" x2="166" y2="18" stroke="var(--border-strong)" stroke-dasharray="3 3" stroke-width="1"/></svg>';
    }

    const rtts = history.filter((s) => s.success).map((s) => Number(s.rtt_ms) || 0);
    const peak = Math.max(10, maxLatencyMs || 0, ...(rtts.length ? rtts : [10]));
    const innerW = width - pad * 2;
    const innerH = height - pad * 2;

    const coords = [];
    const failMarkers = [];
    const denom = Math.max(1, history.length - 1);

    history.forEach((sample, idx) => {
      const x = pad + (idx / denom) * innerW;
      if (!sample.success) {
        failMarkers.push(`<circle cx="${x.toFixed(1)}" cy="${(height - pad).toFixed(1)}" r="2.2" fill="var(--spark-threshold)"/>`);
        return;
      }
      const val = Math.min(peak, Math.max(0, Number(sample.rtt_ms) || 0));
      const y = height - pad - (val / peak) * innerH;
      coords.push(`${x.toFixed(1)},${y.toFixed(1)}`);
    });

    let thresholdLine = "";
    if (maxLatencyMs > 0) {
      const ty = height - pad - (Math.min(peak, maxLatencyMs) / peak) * innerH;
      thresholdLine = `<line x1="${pad}" y1="${ty.toFixed(1)}" x2="${width - pad}" y2="${ty.toFixed(1)}" stroke="var(--spark-threshold)" stroke-opacity="0.55" stroke-dasharray="3 3" stroke-width="1"/>`;
    }

    const polyline = coords.length >= 2
      ? `<polyline fill="none" stroke="var(--spark-line)" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round" points="${coords.join(" ")}"/>`
      : coords.length === 1
      ? `<circle cx="${coords[0].split(",")[0]}" cy="${coords[0].split(",")[1]}" r="2.3" fill="var(--spark-line)"/>`
      : "";

    return `<svg class="sparkline-svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="Broker RTT sparkline">${thresholdLine}${polyline}${failMarkers.join("")}</svg>`;
  }

  async function apiJSON(path, opts) {
    const res = await fetch(path, opts);
    if (!res.ok) {
      const text = await res.text();
      throw new Error(text || `HTTP ${res.status}`);
    }
    return res.json();
  }

  function renderStatus(data) {
    const term = data.terminal || {};
    const pill = statePillMeta(term.state);

    el("version-badge").textContent = "v" + (data.version || "1.0.0");
    el("kpi-state").textContent = pill.title;
    el("kpi-state-pill").textContent = pill.label;
    el("kpi-state-pill").className = "pill " + pill.cls;
    el("kpi-pid").textContent = term.pid > 0 ? "PID " + term.pid : "PID --";

    el("kpi-uptime").textContent = formatDuration(term.uptime_sec);
    el("kpi-restarts").textContent = `${term.restart_count || 0} restarts (${term.restarts_last_hour || 0}/${term.max_restarts_per_hour || 12} last hr)`;

    el("term-status-pill").textContent = pill.label;
    el("term-status-pill").className = "pill " + pill.cls;
    el("term-pid").textContent = term.pid > 0 ? String(term.pid) : "--";
    el("term-uptime").textContent = formatDuration(term.uptime_sec);
    el("term-total-uptime").textContent = formatDuration(term.total_uptime_sec);
    el("term-restarts").textContent = `${term.restart_count || 0} (${term.restarts_last_hour || 0} / ${term.max_restarts_per_hour || 12})`;
    el("term-exit").textContent = String(term.last_exit_code ?? 0);
    el("term-failures").textContent = String(term.consecutive_failures ?? 0);

    const cmdParts = [term.command || "", ...(term.args || [])].filter(Boolean);
    el("term-command").textContent = cmdParts.join(" ") || "--";

    const errBanner = el("term-error-banner");
    if (term.last_error) {
      errBanner.textContent = "Last supervisor error: " + term.last_error;
      errBanner.classList.remove("hidden");
    } else {
      errBanner.classList.add("hidden");
    }

    const novnc = data.novnc_url || "";
    const hdrLink = el("novnc-header-link");
    const cardLink = el("novnc-card-link");
    if (novnc) {
      hdrLink.href = novnc;
      cardLink.href = novnc;
      hdrLink.classList.remove("hidden");
      cardLink.classList.remove("hidden");
    } else {
      hdrLink.classList.add("hidden");
      cardLink.classList.add("hidden");
    }

    const jsum = data.journal_summary || {};
    el("kpi-errors").textContent = String((jsum.errors || 0) + (jsum.warnings || 0));
    el("kpi-journal-meta").textContent = `${jsum.trades || 0} trades | ${jsum.tracked_files || 0} tracked logs`;

    if (data.host) {
      renderHost(data.host);
    }
  }

  function renderLatency(data) {
    const brokers = data.brokers || [];
    const list = el("broker-list");

    if (brokers.length === 0) {
      list.innerHTML = '<div class="empty-state">No brokers configured in tradebox.json.</div>';
      el("kpi-rtt").textContent = "-- ms";
      el("kpi-brokers").textContent = "0 endpoints monitored";
      return;
    }

    const healthyCount = brokers.filter((b) => b.status === "healthy").length;
    const validP50 = brokers.filter((b) => b.sample_count > 0 && b.p50_ms > 0).map((b) => b.p50_ms);
    if (validP50.length > 0) {
      const avgP50 = validP50.reduce((a, b) => a + b, 0) / validP50.length;
      el("kpi-rtt").textContent = avgP50.toFixed(1) + " ms";
    } else {
      el("kpi-rtt").textContent = "-- ms";
    }
    el("kpi-brokers").textContent = `${healthyCount}/${brokers.length} endpoints healthy`;

    list.innerHTML = brokers
      .map((b) => {
        const pill = brokerPillMeta(b.status);
        const lastStr = b.status === "down" ? "DOWN" : `${Number(b.last_ms || 0).toFixed(1)} ms`;
        const spark = buildSparklineSVG(b.history || [], b.max_latency_ms);
        return `
          <div class="broker-row">
            <div class="broker-top">
              <div>
                <div class="broker-name">${escapeHTML(b.name)}</div>
                <div class="broker-host mono">${escapeHTML(b.host)}:${b.port} (max ${b.max_latency_ms} ms)</div>
              </div>
              <span class="pill ${pill.cls}">${pill.label}</span>
            </div>
            <div class="broker-metrics">
              <div class="metric-chips mono">
                <span>last <strong>${escapeHTML(lastStr)}</strong></span>
                <span>p50 <strong>${Number(b.p50_ms || 0).toFixed(1)}ms</strong></span>
                <span>p95 <strong>${Number(b.p95_ms || 0).toFixed(1)}ms</strong></span>
                <span>jitter <strong>${Number(b.jitter_ms || 0).toFixed(1)}ms</strong></span>
                <span>loss <strong>${Number(b.loss_pct || 0).toFixed(0)}%</strong></span>
              </div>
              ${spark}
            </div>
          </div>
        `;
      })
      .join("");
  }

  function renderJournal(data) {
    const entries = data.entries || [];
    const files = data.files || [];
    const tbody = el("journal-tbody");

    if (files.length > 0) {
      const encList = [...new Set(files.map((f) => f.encoding).filter(Boolean))].join(", ");
      el("journal-files-meta").textContent = `Tracking ${files.length} log file(s) (${encList || "UTF-16LE / UTF-8"}) with incremental byte offsets`;
    }

    if (entries.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty-cell">No MT5 journal entries match this filter.</td></tr>';
      return;
    }

    tbody.innerHTML = entries
      .map((e) => {
        const sev = severityPillMeta(e.severity);
        const ruleTag = e.rule_id ? `<span class="rule-tag mono">${escapeHTML(e.rule_id)}</span>` : "";
        return `
          <tr>
            <td class="mono">${escapeHTML(e.timestamp || formatTime(e.observed_at))}</td>
            <td><span class="pill ${sev.cls}">${sev.label}</span></td>
            <td class="mono">${escapeHTML(e.category)}</td>
            <td>${escapeHTML(e.source || e.file)}</td>
            <td>${escapeHTML(e.message)}${ruleTag}</td>
            <td class="mono">${escapeHTML(e.encoding || "UTF-8")}</td>
          </tr>
        `;
      })
      .join("");
  }

  function renderAlerts(data) {
    const ch = data.channels || {};
    const items = [
      { label: "Telegram", on: !!ch.telegram },
      { label: "Discord", on: !!ch.discord },
      { label: "Slack", on: !!ch.slack },
      { label: "HMAC Webhook", on: !!ch.webhook },
    ];
    el("channel-strip").innerHTML = items
      .map((item) => `<span class="pill ${item.on ? "pill-healthy" : "pill-neutral"}">${item.label}: ${item.on ? "ACTIVE" : "OFF"}</span>`)
      .join("") + `<span class="pill pill-info">Cooldown: ${ch.cooldown_sec ?? 0}s</span>`;

    const history = data.history || [];
    const container = el("alert-history");
    if (history.length === 0) {
      container.innerHTML = '<div class="empty-state">No alerts dispatched yet. Click "Send Test Alert" to verify routing.</div>';
      return;
    }

    container.innerHTML = history
      .map((rec) => {
        const a = rec.alert || {};
        const statusCls = rec.status === "sent" ? "pill-healthy" : rec.status === "throttled" ? "pill-warn" : rec.status === "failed" ? "pill-alert" : "pill-neutral";
        return `
          <div class="timeline-item">
            <div class="timeline-top">
              <span class="timeline-title">${escapeHTML(a.title || "Alert")}</span>
              <span class="pill ${statusCls}">${escapeHTML((rec.status || "unknown").toUpperCase())}</span>
            </div>
            <div class="timeline-msg">${escapeHTML(a.message || "")}</div>
            <div class="plugin-meta mono">
              <span>${escapeHTML(a.category || "system")} | ${escapeHTML(a.rule_key || "")}</span>
              <span>${escapeHTML(formatTime(rec.created_at))}</span>
            </div>
          </div>
        `;
      })
      .join("");
  }

  function renderHost(host) {
    el("host-summary-line").textContent = `${host.hostname} (${host.os}/${host.arch}) - ${host.cpu_cores} cores, ${host.total_ram_mb || "?"} MB RAM`;
    const pill = el("host-ready-pill");
    pill.textContent = host.ready ? "READY" : "CHECK WARNINGS";
    pill.className = "pill " + (host.ready ? "pill-healthy" : "pill-warn");

    const checks = host.checks || [];
    el("host-checks").innerHTML = checks
      .map((c) => `
        <div class="check-row">
          <div>
            <div class="check-name">${escapeHTML(c.name)}</div>
            <div class="check-detail">${escapeHTML(c.detail)}</div>
          </div>
          <span class="pill ${c.status === "pass" ? "pill-healthy" : "pill-warn"}">${escapeHTML((c.status || "pass").toUpperCase())}</span>
        </div>
      `)
      .join("");

    if (Array.isArray(host.all_guides) && host.all_guides.length > 0) {
      if (state.hostGuides.length === 0 && host.os) {
        state.selectedOS = host.os === "linux" || host.os === "darwin" ? host.os : "windows";
        syncOSTabs();
      }
      state.hostGuides = host.all_guides;
      renderSelectedPowerGuide();
    }
  }

  function syncOSTabs() {
    document.querySelectorAll("#os-tabs .seg-btn").forEach((btn) => {
      btn.classList.toggle("active", btn.getAttribute("data-os") === state.selectedOS);
    });
  }

  function renderSelectedPowerGuide() {
    const guide = state.hostGuides.find((g) => g.platform === state.selectedOS) || state.hostGuides[0];
    if (!guide) return;
    el("power-notes").textContent = guide.notes || "";
    const keep = (guide.keep_awake_commands || []).join("\n");
    const restore = (guide.restore_commands || []).join("\n");
    el("power-commands").textContent = `# Enable 24/7 keep-awake\n${keep}\n\n# Restore normal sleep\n${restore}`;
  }

  function renderPlugins(data) {
    const list = data.plugins || [];
    el("plugin-count-pill").textContent = `${list.length} Validated`;
    const grid = el("plugins-grid");
    if (list.length === 0) {
      grid.innerHTML = '<div class="empty-state">No validated plugins loaded from configured plugins directory.</div>';
      return;
    }

    grid.innerHTML = list
      .map((p) => `
        <div class="plugin-card">
          <div class="plugin-top">
            <span class="plugin-name">${escapeHTML(p.name)}</span>
            <span class="pill pill-info">${escapeHTML((p.category || "preset").toUpperCase())}</span>
          </div>
          <p class="plugin-desc">${escapeHTML(p.description)}</p>
          <div class="plugin-meta mono">
            <span>${escapeHTML(p.id)} v${escapeHTML(p.version)}</span>
            <span>${(p.rules || []).length} rule(s)</span>
          </div>
        </div>
      `)
      .join("");
  }

  async function refreshAll() {
    if (state.refreshing) return;
    state.refreshing = true;
    try {
      const [statusData, latencyData, journalData, alertsData, pluginsData] = await Promise.all([
        apiJSON("/api/status"),
        apiJSON("/api/latency"),
        apiJSON(`/api/journal?filter=${encodeURIComponent(state.journalFilter)}&limit=80`),
        apiJSON("/api/alerts"),
        apiJSON("/api/plugins"),
      ]);
      renderStatus(statusData);
      renderLatency(latencyData);
      renderJournal(journalData);
      renderAlerts(alertsData);
      renderPlugins(pluginsData);
      el("sync-indicator").textContent = "Updated " + formatTime(new Date().toISOString());
    } catch (err) {
      el("sync-indicator").textContent = "Sync error";
    } finally {
      state.refreshing = false;
    }
  }

  async function terminalAction(action) {
    try {
      await apiJSON(`/api/terminal/${action}`, { method: "POST" });
      await refreshAll();
    } catch (err) {
      const banner = el("term-error-banner");
      banner.textContent = `Action ${action} failed: ${err.message}`;
      banner.classList.remove("hidden");
    }
  }

  function wireControls() {
    el("btn-start").addEventListener("click", () => terminalAction("start"));
    el("btn-restart").addEventListener("click", () => terminalAction("restart"));
    el("btn-stop").addEventListener("click", () => terminalAction("stop"));

    el("btn-probe-now").addEventListener("click", async () => {
      const btn = el("btn-probe-now");
      btn.disabled = true;
      btn.textContent = "Probing...";
      try {
        const data = await apiJSON("/api/latency/probe", { method: "POST" });
        renderLatency(data);
      } finally {
        btn.disabled = false;
        btn.textContent = "Probe Now";
      }
    });

    el("btn-test-alert").addEventListener("click", async () => {
      const btn = el("btn-test-alert");
      const feedback = el("alert-feedback");
      btn.disabled = true;
      try {
        const rec = await apiJSON("/api/alerts/test", { method: "POST" });
        feedback.textContent = `Test alert result: ${rec.status} (${(rec.channels || []).length} channel(s) attempted).`;
        feedback.classList.remove("hidden");
        await refreshAll();
      } catch (err) {
        feedback.textContent = "Test alert failed: " + err.message;
        feedback.classList.remove("hidden");
      } finally {
        btn.disabled = false;
      }
    });

    document.querySelectorAll("[data-filter]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        state.journalFilter = btn.getAttribute("data-filter") || "all";
        document.querySelectorAll("[data-filter]").forEach((b) => b.classList.toggle("active", b === btn));
        const journalData = await apiJSON(`/api/journal?filter=${encodeURIComponent(state.journalFilter)}&limit=80`);
        renderJournal(journalData);
      });
    });

    document.querySelectorAll("#os-tabs .seg-btn").forEach((btn) => {
      btn.addEventListener("click", () => {
        state.selectedOS = btn.getAttribute("data-os") || "windows";
        syncOSTabs();
        renderSelectedPowerGuide();
      });
    });
  }

  initTheme();
  wireControls();
  refreshAll();
  setInterval(refreshAll, 3000);
})();
