// The AnsibleForms RTE builder : builds the RTE images an AnsibleForms controller defines (its
// Designer's RTE images) and pushes them to their registry. It dials the controller and keeps one
// WebSocket open (src/socket/dial.js), is handed one build at a time (src/builder.js), and builds
// with BuildKit (src/buildkit.js). It never reads the controller's database nor its repositories,
// and runs no playbook.
import fs from "fs";
import path from "path";
import http from "http";
import logger from "./src/logger.js";
import { installShutdown, onShutdown } from "./src/shutdown.js";
import { settingsProblems, builderName, version, workDir, buildkitHost, port } from "./src/settings.js";
import { startBuildkit } from "./src/buildkit.js";
import { attach, sendHealth, resumeSpools, isWelcomed } from "./src/builder.js";
import { dialOut } from "./src/socket/dial.js";

installShutdown();
const problems = settingsProblems();
if (problems.length) {
  console.error(`Builder: ${problems.join(" ; ")} ; refusing to start`);
  process.exit(1);
}
fs.mkdirSync(path.join(workDir(), "spool"), { recursive: true, mode: 0o700 });
resumeSpools();
await startBuildkit();
// its container's probes : alive, and ready once the controller welcomed it
const server = http.createServer((req, res) => {
  const ok = req.url === "/live" || (req.url === "/ready" && isWelcomed());
  res.writeHead(ok ? 200 : 503).end(ok ? "ok" : "not ready");
});
server.listen(port(), process.env.LISTEN_HOST || "0.0.0.0");
onShutdown("listener", () => server.close());
setInterval(sendHealth, 20000).unref();
logger.notice(`Builder '${builderName()}' ${version} starting, BuildKit at ${buildkitHost()}`);
const stop = dialOut(process.env.RTE_TOKEN, attach);
onShutdown("socket", () => stop());
