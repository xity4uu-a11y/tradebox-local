#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const VERSION = "1.0.0";

function printHelp() {
  console.log(`create-tradebox-local v${VERSION}
Zero-dependency initializer for tradebox-local 24/7 MetaTrader 5 setups.

Usage:
  create-tradebox-local [options]

Options:
  --mode <laptop|docker>   Target profile (default: laptop)
  --dir <path>             Target output directory (default: current directory)
  --broker-host <host>     Primary broker TCP host (default: access.metatrader5.com)
  --broker-port <port>     Primary broker TCP port (default: 443)
  --max-latency-ms <ms>    Broker latency threshold in ms (default: 120)
  --novnc-url <url>        Optional noVNC remote desktop URL
  --force                  Overwrite existing tradebox.json if present
  --dry-run                Preview generated files without writing to disk
  --version, -v            Print initializer version
  --help, -h               Show this help message
`);
}

function parseArgs(argv) {
  const opts = {
    mode: "laptop",
    dir: ".",
    brokerHost: "access.metatrader5.com",
    brokerPort: 443,
    maxLatencyMs: 120,
    novncUrl: "http://127.0.0.1:6080/vnc.html?autoconnect=1&resize=scale",
    force: false,
    dryRun: false,
    showHelp: false,
    showVersion: false,
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    switch (arg) {
      case "--help":
      case "-h":
        opts.showHelp = true;
        break;
      case "--version":
      case "-v":
        opts.showVersion = true;
        break;
      case "--dry-run":
        opts.dryRun = true;
        break;
      case "--force":
        opts.force = true;
        break;
      case "--mode":
        opts.mode = argv[++i] || "laptop";
        break;
      case "--dir":
        opts.dir = argv[++i] || ".";
        break;
      case "--broker-host":
        opts.brokerHost = argv[++i] || opts.brokerHost;
        break;
      case "--broker-port":
        opts.brokerPort = Number(argv[++i] || 443);
        break;
      case "--max-latency-ms":
        opts.maxLatencyMs = Number(argv[++i] || 120);
        break;
      case "--novnc-url":
        opts.novncUrl = argv[++i] || opts.novncUrl;
        break;
      default:
        throw new Error(`Unknown option: ${arg}`);
    }
  }

  if (opts.mode !== "laptop" && opts.mode !== "docker") {
    throw new Error(`Invalid --mode "${opts.mode}". Expected "laptop" or "docker".`);
  }
  if (!Number.isInteger(opts.brokerPort) || opts.brokerPort < 1 || opts.brokerPort > 65535) {
    throw new Error(`Invalid --broker-port "${opts.brokerPort}". Expected 1..65535.`);
  }
  if (!Number.isFinite(opts.maxLatencyMs) || opts.maxLatencyMs < 1) {
    throw new Error(`Invalid --max-latency-ms "${opts.maxLatencyMs}". Expected >= 1.`);
  }
  return opts;
}

function buildConfig(opts) {
  const isDocker = opts.mode === "docker";
  const isWindows = process.platform === "win32" && !isDocker;

  return {
    terminal_command: isWindows ? "terminal64.exe" : "wine",
    terminal_args: isWindows
      ? ["/portable"]
      : ["/mt5-data/terminal/terminal64.exe", "/portable"],
    working_dir: isDocker ? "/mt5-data/terminal" : ".",
    auto_start: false,
    mt5_logs_dir: isDocker ? "/mt5-data/terminal" : "./mt5-logs",
    brokers: [
      {
        name: "Primary-Broker",
        host: opts.brokerHost,
        port: opts.brokerPort,
        max_latency_ms: opts.maxLatencyMs,
        timeout_ms: 3000,
      },
      {
        name: "Network-Baseline-CF",
        host: "1.1.1.1",
        port: 443,
        max_latency_ms: 80,
        timeout_ms: 3000,
      },
    ],
    restart_policy: {
      max_restarts_per_hour: 12,
      backoff_base_sec: 2,
      backoff_max_sec: 60,
      check_interval_sec: 5,
      stable_reset_sec: 60,
    },
    latency_check_sec: 15,
    journal_poll_sec: 2,
    alerts: {
      cooldown_sec: 300,
      telegram: {
        enabled: false,
        bot_token: "${TRADEBOX_TELEGRAM_BOT_TOKEN}",
        chat_id: "${TRADEBOX_TELEGRAM_CHAT_ID}",
        api_base_url: "https://api.telegram.org",
      },
      discord: {
        enabled: false,
        webhook_url: "${TRADEBOX_DISCORD_WEBHOOK_URL}",
      },
      slack: {
        enabled: false,
        webhook_url: "${TRADEBOX_SLACK_WEBHOOK_URL}",
      },
      webhook: {
        enabled: false,
        url: "${TRADEBOX_WEBHOOK_URL}",
        secret: "${TRADEBOX_WEBHOOK_SECRET}",
      },
    },
    novnc_url: opts.novncUrl,
    http_addr: isDocker ? "0.0.0.0:8092" : "127.0.0.1:8092",
    plugins_dir: isDocker ? "/opt/tradebox/plugins" : "./plugins",
  };
}

function buildSampleUTF16LELog() {
  const lines = [
    "CS\t0\t08:00:01.104\tNetwork\t'50192831': authorized on MetaQuotes-Demo through Access Server EU (ping: 14.20 ms)",
    "CJ\t0\t08:15:00.221\tTrades\t'50192831': instant buy 0.10 EURUSD at 1.08420 filled (deal #44182011)",
    "EO\t2\t09:05:11.903\tTrendBreakoutEA (EURUSD,M15)\tOrderSend failed: Error 4756 (Trade request sending failed)",
    "CJ\t2\t09:05:12.104\tTrades\t'50192831': failed instant sell 0.10 EURUSD [AutoTrading disabled by client]",
    "CS\t2\t09:12:48.550\tNetwork\t'50192831': connection to MetaQuotes-Demo lost - No connection",
  ];
  const text = lines.join("\r\n") + "\r\n";
  const bom = Buffer.from([0xff, 0xfe]);
  const body = Buffer.from(text, "utf16le");
  return Buffer.concat([bom, body]);
}

function buildEnvExample() {
  return `# Optional alert credentials read by tradebox.json via \${VAR_NAME} expansion
TRADEBOX_TELEGRAM_BOT_TOKEN=
TRADEBOX_TELEGRAM_CHAT_ID=
TRADEBOX_DISCORD_WEBHOOK_URL=
TRADEBOX_SLACK_WEBHOOK_URL=
TRADEBOX_WEBHOOK_URL=
TRADEBOX_WEBHOOK_SECRET=
`;
}

function main() {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(`Error: ${err.message}`);
    process.exit(1);
  }

  if (opts.showVersion) {
    console.log(`create-tradebox-local v${VERSION}`);
    process.exit(0);
  }
  if (opts.showHelp) {
    printHelp();
    process.exit(0);
  }

  const targetDir = path.resolve(opts.dir);
  const configPath = path.join(targetDir, "tradebox.json");
  const envExamplePath = path.join(targetDir, ".env.example");
  const today = new Date().toISOString().slice(0, 10).replaceAll("-", "");
  const sampleLogPath = path.join(targetDir, "mt5-logs", "MQL5", "Logs", `${today}.log`);

  const cfg = buildConfig(opts);
  const cfgJSON = JSON.stringify(cfg, null, 2) + "\n";

  if (opts.dryRun) {
    console.log(`[dry-run] Mode: ${opts.mode}`);
    console.log(`[dry-run] Would write: ${configPath}`);
    console.log(`[dry-run] Would write: ${envExamplePath}`);
    console.log(`[dry-run] Would write UTF-16LE BOM sample log: ${sampleLogPath}`);
    console.log("\n--- tradebox.json preview ---");
    console.log(cfgJSON);
    process.exit(0);
  }

  if (fs.existsSync(configPath) && !opts.force) {
    console.error(`Error: ${configPath} already exists. Re-run with --force to overwrite.`);
    process.exit(1);
  }

  fs.mkdirSync(path.dirname(sampleLogPath), { recursive: true });
  fs.writeFileSync(configPath, cfgJSON, "utf8");
  fs.writeFileSync(envExamplePath, buildEnvExample(), "utf8");
  fs.writeFileSync(sampleLogPath, buildSampleUTF16LELog());

  console.log(`Initialized tradebox-local (${opts.mode} profile):`);
  console.log(`  Config:     ${configPath}`);
  console.log(`  Env sample: ${envExamplePath}`);
  console.log(`  MT5 log:    ${sampleLogPath} (UTF-16LE BOM)`);
  if (opts.mode === "docker") {
    console.log("\nNext steps:\n  docker compose up -d\n  Open http://127.0.0.1:8092");
  } else {
    console.log("\nNext steps:\n  tradebox host-check\n  tradebox serve --config tradebox.json --addr 127.0.0.1:8092");
  }
}

main();
