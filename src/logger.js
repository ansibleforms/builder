// The builder's log : one line per message on stdout, its level and time first. LOG_LEVEL
// (debug, info, notice, warning, error ; notice by default) says from which level on.
const LEVELS = ["debug", "info", "notice", "warning", "error", "crit"];
const threshold = () => Math.max(0, LEVELS.indexOf(String(process.env.LOG_LEVEL || "notice").toLowerCase()));

/**
 * Writes a line at a level, when the level is at or above LOG_LEVEL.
 *
 * Args:
 *   level (string): the level.
 *   parts (any[]): what to write (an Error : its message).
 */
function write(level, parts) {
  if (LEVELS.indexOf(level) < threshold()) return;
  const text = parts.map((p) => (p instanceof Error ? p.message : typeof p === "string" ? p : JSON.stringify(p))).join(" ");
  const line = `${new Date().toISOString()} ${level}: ${text}`;
  if (LEVELS.indexOf(level) >= LEVELS.indexOf("error")) console.error(line);
  else console.log(line);
}

const logger = Object.fromEntries(LEVELS.map((level) => [level, (...parts) => write(level, parts)]));
export default logger;
