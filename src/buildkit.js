// The BuildKit a builder builds with : BUILDKIT_HOST when it names one, else a rootless buildkitd
// it starts in its own container (the image has buildkitd, rootlesskit and the subordinate ids).
import fs from "fs";
import path from "path";
import { spawn } from "child_process";
import logger from "./logger.js";
import { onShutdown, isStopping } from "./shutdown.js";
import { workDir } from "./settings.js";

/**
 * Starts its own rootless buildkitd, when no BUILDKIT_HOST is given.
 *
 * Returns:
 *   Promise<boolean>: true once its socket is there ; false after 30 seconds without it.
 */
export async function startBuildkit() {
  if (process.env.BUILDKIT_HOST) return true;
  const dir = path.join(workDir(), "buildkit");
  fs.mkdirSync(dir, { recursive: true });
  const sock = path.join(dir, "buildkitd.sock");
  const child = spawn("rootlesskit", ["buildkitd", "--oci-worker-no-process-sandbox", "--addr", `unix://${sock}`, "--root", path.join(dir, "root")], { stdio: ["ignore", "pipe", "pipe"] });
  child.stderr.on("data", (c) => logger.debug(`buildkitd: ${String(c).trim()}`));
  child.on("error", (e) => logger.error(`buildkitd could not start (${e.message}): set BUILDKIT_HOST to a BuildKit the builder can reach`));
  child.on("exit", (code) => !isStopping() && logger.error(`buildkitd stopped (${code}): the container needs --security-opt seccomp=unconfined --security-opt apparmor=unconfined`));
  onShutdown("buildkitd", () => child.kill("SIGTERM"));
  for (let i = 0; i < 60 && !fs.existsSync(sock); i++) await new Promise((r) => setTimeout(r, 500));
  if (!fs.existsSync(sock)) {
    logger.error("buildkitd did not open its socket within 30 seconds");
    return false;
  }
  return true;
}
