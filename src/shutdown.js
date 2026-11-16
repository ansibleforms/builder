// Stopping : SIGTERM or SIGINT runs what each part registered, then exits.
import logger from "./logger.js";

const closers = [];
let stopping = false;

/** Whether the process is stopping. */
export const isStopping = () => stopping;

/**
 * Registers what to close when the process stops.
 *
 * Args:
 *   name (string): what it is, for the log.
 *   fn (function): closes it (may be async).
 */
export function onShutdown(name, fn) {
  closers.push({ name, fn });
}

/**
 * Stops : every closer run, then the process exits.
 *
 * Args:
 *   signal (string): the signal received.
 */
async function shutdown(signal) {
  if (stopping) return;
  stopping = true;
  logger.notice(`${signal}: stopping`);
  setTimeout(() => process.exit(0), 10000).unref();
  for (const { name, fn } of closers.reverse()) {
    try {
      await fn();
    } catch (e) {
      logger.warning(`Stopping ${name} failed: ${e.message}`);
    }
  }
  process.exit(0);
}

/** Installs the SIGTERM and SIGINT handlers. */
export function installShutdown() {
  process.on("SIGTERM", () => shutdown("SIGTERM"));
  process.on("SIGINT", () => shutdown("SIGINT"));
}
