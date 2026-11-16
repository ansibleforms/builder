// The builder's one socket to the controller : dialed (outbound only : the controller never dials a
// builder), and dialed again when it drops, after redialWait. As the AnsibleForms runner's
// (server/src/rte/socket/rte-socket.js dialOut), with the builder's kind in its headers so the
// controller keeps it as a builder, never a runner.
import os from "os";
import WebSocket from "ws";
import { HttpsProxyAgent } from "https-proxy-agent";
import logger from "../logger.js";
import { isStopping } from "../shutdown.js";
import { nodeId, builderName, version, controllerUrl, controllerTlsVerify } from "../settings.js";
import { Session } from "./session.js";
import { DialReport, describeDialFailure, failureLine, safeUrl, ago } from "./dialReport.js";
import { socketUrl, MAX_PAYLOAD, CLOSE, REFUSED_CLOSES, REFUSED_STATUSES, REFUSED_REDIAL_MS, CONTRACT_REDIAL_MS } from "./protocol.js";

/**
 * What the builder says of itself when it dials : its token, its id and name, its host and version
 * (shown to the admin who approves it), that it is a builder, and its join token when it has one.
 *
 * Args:
 *   token (string): RTE_TOKEN.
 *
 * Returns:
 *   object: the headers.
 */
export function builderHeaders(token) {
  const headers = {
    Authorization: `Bearer ${token}`,
    "X-AF-RTE-Id": nodeId(),
    "X-AF-RTE-Name": builderName(),
    "X-AF-RTE-Host": os.hostname(),
    "X-AF-RTE-Version": version,
    "X-AF-Peer-Kind": "builder",
  };
  if (process.env.RTE_JOIN_TOKEN) headers["X-AF-RTE-Join"] = process.env.RTE_JOIN_TOKEN;
  return headers;
}

/**
 * How long it waits before it dials again : a little longer each time (1 s up to 30 s, with some
 * jitter) ; refused (rejected, removed, a token or join token refused) REFUSED_REDIAL_MS ; another
 * contract (an upgrade in progress) CONTRACT_REDIAL_MS.
 *
 * Args:
 *   close (object): { code, status (the refused handshake's, or null), delay, random }.
 *
 * Returns:
 *   object: { wait, delay, refused }.
 */
export function redialWait({ code, status, delay, random = Math.random() }) {
  if (REFUSED_CLOSES.includes(code) || REFUSED_STATUSES.includes(status)) return { wait: REFUSED_REDIAL_MS, delay: 1000, refused: true };
  if (code === CLOSE.CONTRACT) return { wait: CONTRACT_REDIAL_MS, delay: 1000, refused: true };
  return { wait: delay + Math.floor(random * 1000), delay: Math.min(delay * 2, 30000), refused: false };
}

/**
 * Whether NO_PROXY says to reach a host directly.
 *
 * Args:
 *   host (string): the controller's host name.
 *
 * Returns:
 *   boolean
 */
export function noProxy(host) {
  const list = String(process.env.NO_PROXY || process.env.no_proxy || "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean);
  return list.some((p) => p === "*" || host === p || host.endsWith(p.startsWith(".") ? p : `.${p}`));
}

/**
 * Keeps the socket to the controller open.
 *
 * Args:
 *   token (string): RTE_TOKEN.
 *   attach (function): (session) => takes a socket once it is open.
 *
 * Returns:
 *   function: stops dialing and closes the socket.
 */
export function dialOut(token, attach) {
  const url = socketUrl(controllerUrl());
  let delay = 1000;
  const proxy = process.env.HTTPS_PROXY || process.env.https_proxy || "";
  const options = { headers: builderHeaders(token), maxPayload: MAX_PAYLOAD, handshakeTimeout: 15000, rejectUnauthorized: controllerTlsVerify() };
  if (proxy && !noProxy(new URL(url).hostname)) options.agent = new HttpsProxyAgent(proxy);
  const shown = safeUrl(url);
  const report = new DialReport();
  const secrets = [token, process.env.RTE_JOIN_TOKEN];
  let stopped = false;
  let current = null;
  const connect = () => {
    if (isStopping() || stopped) return;
    const ws = new WebSocket(url, options);
    current = ws;
    let refusedStatus = null;
    let failure = null;
    let opened = false;
    ws.on("open", () => {
      opened = true;
      delay = 1000;
      const back = report.connected();
      if (back) logger.notice(`Builder '${builderName()}': reached the controller at ${shown} again, after ${back.failures} failed dial(s) in ${ago(Date.now() - back.since)}`);
      attach(new Session(ws, "app"));
    });
    ws.on("unexpected-response", (req, res) => {
      refusedStatus = res.statusCode;
      let body = "";
      res.on("data", (c) => (body += c));
      res.on("end", () => {
        failure = { status: res.statusCode, body: body.slice(0, 2000) };
        ws.terminate();
      });
      res.on("error", () => ws.terminate());
    });
    ws.on("error", (err) => {
      if (!failure && !refusedStatus) failure = { error: err };
    });
    ws.on("close", (code) => {
      if (isStopping() || stopped) return;
      const next = redialWait({ code, status: refusedStatus, delay });
      delay = next.delay;
      const again = next.wait >= 60000 ? `${Math.round(next.wait / 60000)} minute(s)` : `${Math.round(next.wait / 1000)} seconds`;
      if (!opened) {
        const why = describeDialFailure(failure || { status: refusedStatus, code }, { side: "app", url, proxy: !!options.agent, secrets });
        const line = failureLine(report, why, `Builder '${builderName()}': cannot reach the controller at ${shown}`);
        if (line) logger.warning(`${line}: dialing again in ${again}`);
      } else if (next.refused) {
        logger.warning(`Builder '${builderName()}': the controller refused this builder (${refusedStatus || code}): dialing again in ${again}, or restart it once an admin acted`);
      } else {
        logger.info(`Builder: dialing the controller again in ${again}`);
      }
      setTimeout(connect, next.wait).unref?.();
    });
  };
  logger.notice(`Builder '${builderName()}': dialing the controller at ${shown}${options.agent ? " through the proxy" : ""}`);
  connect();
  return () => {
    stopped = true;
    current?.terminate();
  };
}
