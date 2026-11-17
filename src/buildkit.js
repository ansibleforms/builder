// The BuildKit a builder builds with : BUILDKIT_HOST when it names one, else a rootless buildkitd
// it starts in its own container (the image has buildkitd, rootlesskit and the subordinate ids).
import fs from "fs";
import path from "path";
import { spawn, execFile } from "child_process";
import logger from "./logger.js";
import { onShutdown, isStopping } from "./shutdown.js";
import { workDir, buildkitHost } from "./settings.js";

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

// ===========================================================================
// ITS PLATFORMS
// ===========================================================================

// the platforms an image's arch asks for : one image, or one index with both
export const ARCH_PLATFORMS = { amd64: ["linux/amd64"], arm64: ["linux/arm64"], all: ["linux/amd64", "linux/arm64"] };

// how to let a host build another architecture : QEMU registered on it, once
export const QEMU_HINT = "docker run --privileged --rm tonistiigi/binfmt --install all";

/**
 * The platforms a build's arch asks for.
 *
 * Args:
 *   arch (string): amd64, arm64 or all ; none (a controller of before) : the builder's own.
 *
 * Returns:
 *   string[]: the platforms (linux/amd64...) ; empty for the builder's own.
 *
 * Raises:
 *   Error: an arch that is none of them.
 */
export function platformsOf(arch) {
  if (arch === undefined || arch === null || arch === "") return [];
  const platforms = ARCH_PLATFORMS[arch];
  if (!platforms) throw new Error(`'${arch}' is no architecture: amd64, arm64 or all`);
  return platforms;
}

/**
 * The platforms BuildKit's workers can build, from buildctl debug workers' JSON : another
 * architecture is among them only when QEMU is registered on the host.
 *
 * Args:
 *   workers (object[]): the workers, as buildctl prints them.
 *
 * Returns:
 *   string[]: os/architecture, each once.
 */
export function platformsOfWorkers(workers) {
  const out = new Set();
  for (const w of Array.isArray(workers) ? workers : []) for (const p of w.platforms || []) if (p.os && p.architecture) out.add(`${p.os}/${p.architecture}`);
  return [...out];
}

/**
 * The platforms BuildKit can build now.
 *
 * Returns:
 *   Promise<string[]|null>: os/architecture ; null when BuildKit could not say.
 */
export function buildablePlatforms() {
  return new Promise((resolve) => {
    execFile("buildctl", ["--addr", buildkitHost(), "debug", "workers", "--format", "{{json .}}"], { timeout: 20000 }, (err, stdout) => {
      if (err) return resolve(null);
      try {
        resolve(platformsOfWorkers(JSON.parse(stdout)));
      } catch {
        resolve(null);
      }
    });
  });
}
