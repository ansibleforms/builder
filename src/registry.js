// The registry login a build pushes with : a docker config.json of its own, in the build's
// temporary folder (DOCKER_CONFIG), read by buildctl and cosign, removed with the folder.
import fs from "fs";
import path from "path";

/**
 * The registry's host, as a docker config names it : its address without scheme nor path.
 *
 * Args:
 *   url (string): the registry credential's address (https://registry.example.com).
 *
 * Returns:
 *   string: registry.example.com
 */
export const registryHost = (url) => String(url || "").replace(/^[a-z]+:\/\//i, "").replace(/\/.*$/, "");

/**
 * Writes the docker config with the registry's login.
 *
 * Args:
 *   dir (string): the folder it goes in.
 *   registry (object|null): { url, user, password }.
 *
 * Returns:
 *   string: the folder (DOCKER_CONFIG).
 */
export function writeDockerConfig(dir, registry) {
  const auths = {};
  if (registry?.url) auths[registryHost(registry.url)] = { auth: Buffer.from(`${registry.user || ""}:${registry.password || ""}`).toString("base64") };
  fs.writeFileSync(path.join(dir, "config.json"), JSON.stringify({ auths }), { mode: 0o600 });
  return dir;
}

/**
 * Whether a registry is reached over plain http (its address says http://) : pushed to without TLS.
 *
 * Args:
 *   registry (object|null): { url }.
 *
 * Returns:
 *   boolean
 */
export const insecureRegistry = (registry) => /^http:\/\//i.test(registry?.url || "");
