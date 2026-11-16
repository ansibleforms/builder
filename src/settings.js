// The builder's settings, from its environment.
import os from "os";
import path from "path";
import { readFileSync } from "fs";
import { fileURLToPath } from "url";

// its own version (package.json), said in its hello and its join request
export const version = JSON.parse(readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "package.json"), "utf8")).version;

// the name it goes by among the controller's nodes : builder-<hostname>-<port>, the same after a
// restart of the same container
export const nodeId = () => `builder-${os.hostname()}-${process.env.PORT || 8000}`;

// its name in the Designer's RTE builders (RTE_NAME), its node id by default
export const builderName = () => String(process.env.RTE_NAME || "").trim() || nodeId();

// the controller's address it dials (CONTROLLER_URL, APP_URL before 7.6)
export const controllerUrl = () => process.env.CONTROLLER_URL || process.env.APP_URL || "";

// whether it checks the controller's certificate (CONTROLLER_TLS_VERIFY=0 : a self-signed one, in
// one trusted network only)
export const controllerTlsVerify = () => String(process.env.CONTROLLER_TLS_VERIFY ?? process.env.APP_TLS_VERIFY ?? "1") !== "0";

// where its spool, its builds' files and its own buildkitd's cache are
export const workDir = () => process.env.BUILDER_WORK_PATH || path.join(os.tmpdir(), "af-builder");

// the BuildKit it builds with : BUILDKIT_HOST, else the rootless buildkitd it starts itself
export const buildkitHost = () => process.env.BUILDKIT_HOST || `unix://${path.join(workDir(), "buildkit", "buildkitd.sock")}`;

// the port of its /live and /ready
export const port = () => Number(process.env.PORT || 8000);

/**
 * Why the builder cannot start, when it cannot : its token and the controller's address.
 *
 * Returns:
 *   string[]: the problems ; none when it can start.
 */
export function settingsProblems() {
  const problems = [];
  if (String(process.env.RTE_TOKEN || "").length < 16) problems.push("set RTE_TOKEN (at least 16 characters)");
  if (!controllerUrl()) problems.push("set CONTROLLER_URL, the controller's address it dials");
  return problems;
}
