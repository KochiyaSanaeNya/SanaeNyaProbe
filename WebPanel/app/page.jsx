"use client";

import { useCallback, useEffect, useMemo, useState } from "react";

const knownMetrics = new Set([
  "schema_version",
  "name",
  "uuid",
  "timestamp_unix",
  "uptime_sec",
  "load1",
  "load3",
  "load5",
  "mem_total_bytes",
  "mem_used_bytes",
  "mem_available_bytes",
  "mem_used_percent",
  "swap_total_bytes",
  "swap_used_bytes",
  "swap_free_bytes",
  "swap_used_percent",
  "storage_total_bytes",
  "storage_used_bytes",
  "storage_free_bytes",
  "storage_available_bytes",
  "storage_used_percent",
  "storage",
  "disk_rates_ready",
  "net_rates_ready",
  "disk_read_bytes",
  "disk_write_bytes",
  "disk_read_bps",
  "disk_write_bps",
  "disk_read_iops",
  "disk_write_iops",
  "disk_device_count",
  "net_rx_bytes",
  "net_tx_bytes",
  "net_rx_bps",
  "net_tx_bps",
  "net_interface_count",
  "errors",
]);

export default function MonitorPage() {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadedAt, setLoadedAt] = useState(null);

  const loadMonitor = useCallback(async () => {
    setLoading(true);
    setError("");

    try {
      const response = await fetch("/api/monitor", {
        method: "GET",
        headers: { Accept: "application/json" },
        cache: "no-store",
      });
      const payload = await response.json().catch(() => null);

      if (!response.ok) {
        throw new Error(payload?.error || `接口返回 ${response.status} ${response.statusText}`.trim());
      }

      setData(payload);
      setLoadedAt(new Date());
    } catch (err) {
      setData({ servers: [] });
      setError(err instanceof Error ? err.message : String(err));
      setLoadedAt(new Date());
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadMonitor();
  }, [loadMonitor]);

  const servers = Array.isArray(data?.servers) ? data.servers : [];
  const online = servers.filter((server) => Boolean(server.online)).length;
  const offline = servers.length - online;
  const loadState = error
    ? "加载失败"
    : data?.now
      ? `服务端时间 ${formatDateTime(data.now)}`
      : loading
        ? "正在加载"
        : "已加载";

  return (
    <main className="shell">
      <header className="topbar">
        <div>
          <p className="eyebrow">SanaeNyaProbe</p>
          <h1>服务器监控</h1>
        </div>
        <button className="icon-button" type="button" onClick={loadMonitor} disabled={loading} aria-label="刷新数据" title="刷新数据">
          <span aria-hidden="true">↻</span>
        </button>
      </header>

      <section className="summary-grid" aria-label="概要">
        <SummaryItem label="总数" value={servers.length} />
        <SummaryItem label="在线" value={online} />
        <SummaryItem label="离线" value={offline} />
        <SummaryItem label="离线阈值" value={formatSeconds(data?.offline_after_seconds)} />
      </section>

      <section className="status-row" aria-live="polite">
        <span className="load-state">{loadState}</span>
        <span>{loadedAt ? `页面获取时间 ${formatDateTime(loadedAt)}` : "--"}</span>
      </section>

      {error ? <section className="error-panel">{error}</section> : null}

      <section className="server-grid" aria-label="服务器列表">
        {servers.map((server) => (
          <ServerCard key={server.key || server.uuid || server.name} server={server} nowUnix={data?.now_unix} />
        ))}
      </section>

      {!loading && servers.length === 0 ? <section className="empty-state">暂无服务器上报数据</section> : null}
    </main>
  );
}

function SummaryItem({ label, value }) {
  return (
    <article className="summary-item">
      <span className="label">{label}</span>
      <strong>{value ?? "--"}</strong>
    </article>
  );
}

function ServerCard({ server, nowUnix }) {
  const metrics = normalizeMetrics(server.metrics);
  const online = Boolean(server.online);
  const statusText = online ? "在线" : "离线";
  const errors = metric(metrics, "errors");
  const storageRows = parseStorage(metric(metrics, "storage"));

  return (
    <article className="server-card">
      <div className="server-head">
        <div className="server-title">
          <h2>{server.name || "未命名服务器"}</h2>
          <p>{server.uuid || server.key || "--"}</p>
        </div>
        <span className={`pill ${online ? "online" : "offline"}`}>{statusText}</span>
      </div>

      <div className="server-body">
        <div className="facts">
          <Fact label="最后上报" value={formatDateTime(server.last_seen)} />
          <Fact label="距今" value={formatAge(server.last_seen_unix, nowUnix)} />
          <Fact label="运行时长" value={formatDuration(metricNumber(metrics, "uptime_sec"))} />
          <Fact label="离线时间" value={server.offline_since ? formatDateTime(server.offline_since) : "--"} />
        </div>

        {errors ? <div className="warning">{errors}</div> : null}

        <MetricSection title="资源">
          <Meter label="内存" percent={metricNumber(metrics, "mem_used_percent")} detail={bytesPair(metrics, "mem_used_bytes", "mem_total_bytes")} />
          <Meter label="Swap" percent={metricNumber(metrics, "swap_used_percent")} detail={bytesPair(metrics, "swap_used_bytes", "swap_total_bytes")} />
          <Meter label="存储" percent={metricNumber(metrics, "storage_used_percent")} detail={bytesPair(metrics, "storage_used_bytes", "storage_total_bytes")} />
        </MetricSection>

        <MetricSection title="负载">
          <MetricGrid>
            <Mini label="1 分钟" value={formatNumber(metricNumber(metrics, "load1"), 2)} />
            <Mini label="3 分钟" value={formatNumber(metricNumber(metrics, "load3"), 2)} />
            <Mini label="5 分钟" value={formatNumber(metricNumber(metrics, "load5"), 2)} />
          </MetricGrid>
        </MetricSection>

        <MetricSection title="网络">
          <MetricGrid>
            <Mini label="下行" value={formatRate(metricNumber(metrics, "net_rx_bps"))} />
            <Mini label="上行" value={formatRate(metricNumber(metrics, "net_tx_bps"))} />
            <Mini label="网卡" value={metric(metrics, "net_interface_count") || "--"} />
          </MetricGrid>
        </MetricSection>

        <MetricSection title="磁盘">
          <MetricGrid>
            <Mini label="读取" value={formatRate(metricNumber(metrics, "disk_read_bps"))} />
            <Mini label="写入" value={formatRate(metricNumber(metrics, "disk_write_bps"))} />
            <Mini label="设备" value={metric(metrics, "disk_device_count") || "--"} />
            <Mini label="读 IOPS" value={formatNumber(metricNumber(metrics, "disk_read_iops"), 2)} />
            <Mini label="写 IOPS" value={formatNumber(metricNumber(metrics, "disk_write_iops"), 2)} />
            <Mini label="速率状态" value={readyText(metrics)} />
          </MetricGrid>
        </MetricSection>

        {storageRows.length ? (
          <MetricSection title="文件系统">
            <StorageTable rows={storageRows} />
          </MetricSection>
        ) : null}

        <RawDetails metrics={metrics} />
      </div>
    </article>
  );
}

function Fact({ label, value }) {
  return (
    <div className="fact">
      <span className="label">{label}</span>
      <strong>{value || "--"}</strong>
    </div>
  );
}

function MetricSection({ title, children }) {
  return (
    <section className="metric-section">
      <h3 className="metric-title">{title}</h3>
      {children}
    </section>
  );
}

function Meter({ label, percent, detail }) {
  const value = clamp(percent ?? 0, 0, 100);
  const tone = value >= 90 ? "danger" : value >= 75 ? "warn" : "";

  return (
    <div className="meter">
      <div className="meter-line">
        <span>{label}</span>
        <strong>{percent === null ? "--" : `${formatNumber(value, 1)}%`}</strong>
      </div>
      <div className="bar">
        <span className={tone} style={{ width: `${value}%` }} />
      </div>
      <div className="meter-line">
        <span>{detail || "--"}</span>
      </div>
    </div>
  );
}

function MetricGrid({ children }) {
  return <div className="metric-grid">{children}</div>;
}

function Mini({ label, value }) {
  return (
    <div className="mini">
      <span>{label}</span>
      <strong>{value || "--"}</strong>
    </div>
  );
}

function StorageTable({ rows }) {
  return (
    <table className="storage-table">
      <thead>
        <tr>
          <th>挂载点</th>
          <th>文件系统</th>
          <th>已用</th>
          <th>总量</th>
          <th>占用</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((item) => (
          <tr key={`${item.mount_point || ""}-${item.file_system || ""}`}>
            <td>{item.mount_point || "--"}</td>
            <td>{item.file_system || "--"}</td>
            <td>{formatBytes(toNumber(item.used_bytes))}</td>
            <td>{formatBytes(toNumber(item.total_bytes))}</td>
            <td>{formatPercent(toNumber(item.used_percent))}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function RawDetails({ metrics }) {
  const entries = useMemo(
    () => Object.entries(metrics).filter(([key]) => !knownMetrics.has(key)).sort(([a], [b]) => a.localeCompare(b)),
    [metrics],
  );

  if (entries.length === 0) {
    return null;
  }

  return (
    <details className="raw-details">
      <summary>其他指标</summary>
      <table className="raw-table">
        <tbody>
          {entries.map(([key, value]) => (
            <tr key={key}>
              <th>{key}</th>
              <td>{Array.isArray(value) ? value.join(", ") : String(value)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

function normalizeMetrics(metrics) {
  if (!metrics || typeof metrics !== "object") {
    return {};
  }
  return metrics;
}

function metric(metrics, key) {
  const value = metrics[key];
  if (Array.isArray(value)) {
    return value.length > 0 ? String(value[0]) : "";
  }
  if (value === null || value === undefined) {
    return "";
  }
  return String(value);
}

function metricNumber(metrics, key) {
  const value = Number(metric(metrics, key));
  return Number.isFinite(value) ? value : null;
}

function parseStorage(value) {
  if (!value) {
    return [];
  }
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function bytesPair(metrics, usedKey, totalKey) {
  const used = metricNumber(metrics, usedKey);
  const total = metricNumber(metrics, totalKey);
  if (used === null && total === null) {
    return "--";
  }
  return `${formatBytes(used)} / ${formatBytes(total)}`;
}

function readyText(metrics) {
  const disk = metric(metrics, "disk_rates_ready") === "1" ? "磁盘就绪" : "磁盘采样中";
  const net = metric(metrics, "net_rates_ready") === "1" ? "网络就绪" : "网络采样中";
  return `${disk} · ${net}`;
}

function formatDateTime(value) {
  if (!value) {
    return "--";
  }
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "--";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function formatAge(lastSeenUnix, nowUnix) {
  if (!Number.isFinite(Number(lastSeenUnix))) {
    return "--";
  }
  const now = Number.isFinite(Number(nowUnix)) ? Number(nowUnix) : Math.floor(Date.now() / 1000);
  const seconds = Math.max(0, now - Number(lastSeenUnix));
  return `${formatSeconds(seconds)}前`;
}

function formatSeconds(value) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds)) {
    return "--";
  }
  if (seconds < 60) {
    return `${Math.round(seconds)} 秒`;
  }
  if (seconds < 3600) {
    return `${Math.floor(seconds / 60)} 分 ${Math.round(seconds % 60)} 秒`;
  }
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return `${hours} 小时 ${minutes} 分`;
}

function formatDuration(seconds) {
  if (seconds === null) {
    return "--";
  }
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) {
    return `${days} 天 ${hours} 小时`;
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分`;
  }
  return `${minutes} 分`;
}

function formatBytes(value) {
  if (value === null || value === undefined || !Number.isFinite(Number(value))) {
    return "--";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let size = Math.max(0, Number(value));
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${formatNumber(size, index === 0 ? 0 : 2)} ${units[index]}`;
}

function formatRate(value) {
  if (value === null) {
    return "--";
  }
  return `${formatBytes(value)}/s`;
}

function formatPercent(value) {
  if (value === null) {
    return "--";
  }
  return `${formatNumber(value, 1)}%`;
}

function formatNumber(value, digits) {
  if (value === null || value === undefined || !Number.isFinite(Number(value))) {
    return "--";
  }
  return Number(value).toLocaleString("zh-CN", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

function toNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function clamp(value, min, max) {
  return Math.min(max, Math.max(min, value));
}
