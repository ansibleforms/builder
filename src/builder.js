// The builds : one at a time, handed over the socket (dial.js). Each one's files written, built
// with buildctl against BuildKit (buildkit.js), pushed with the registry's login (registry.js),
// signed with cosign when asked ; its output streamed back as the job's, through a spool
// (socket/spool.js) : what the controller did not ack is sent again after a reconnect.
import fs from "fs";
import path from "path";
import { spawn } from "child_process";
import logger from "./logger.js";
import { isStopping } from "./shutdown.js";
import { nodeId, builderName, version, workDir, buildkitHost } from "./settings.js";
import { writeDockerConfig, insecureRegistry } from "./registry.js";
import { openJobSecrets } from "./socket/sealedSecrets.js";
import { SOCKET_CONTRACT, WINDOW, CLOSE } from "./socket/protocol.js";
import { JobSpool } from "./socket/spool.js";

// ===========================================================================
// STATE
// ===========================================================================

// the socket to the controller, and whether it welcomed this builder
let session = null;
let welcomed = false;
// the builds it holds : jobId -> { spool, running, child, aborted }
const builds = new Map();

/**
 * What it can take : one build at a time.
 *
 * Returns:
 *   object: { max, running, free }.
 */
function capacity() {
  const running = [...builds.values()].filter((b) => b.running).length;
  return { max: 1, running, free: Math.max(0, 1 - running) };
}

// ===========================================================================
// ITEMS TO THE CONTROLLER
// ===========================================================================

/**
 * Sends a build's items not sent yet, within the window of items not acked.
 *
 * Args:
 *   jobId (number): the build's job.
 */
function pump(jobId) {
  const b = builds.get(jobId);
  if (!b || !session || !welcomed) return;
  const sp = b.spool;
  while (sp.next <= sp.seq && sp.next - sp.acked <= WINDOW) {
    const item = sp.get(sp.next);
    sp.next++;
    if (item && !session.send({ t: "item", jobId, item })) {
      sp.rewind();
      return;
    }
  }
}

/**
 * Adds an item to a build's spool and sends it.
 *
 * Args:
 *   jobId (number): the build's job.
 *   item (object): { kind, ... }.
 */
function emit(jobId, item) {
  const b = builds.get(jobId);
  if (!b) return;
  b.spool.append(item);
  pump(jobId);
}

/**
 * A line of the build's output, masked of its secrets.
 *
 * Args:
 *   jobId (number): the build's job.
 *   text (string): the line(s).
 *   stream (string): stdout or stderr.
 */
function say(jobId, text, stream = "stdout") {
  const b = builds.get(jobId);
  if (!b || !text) return;
  let output = String(text);
  for (const s of b.secrets) if (s) output = output.split(s).join("********");
  b.order += 1;
  emit(jobId, { kind: "output", record: { output, output_type: stream, order: b.order } });
}

// ===========================================================================
// BUILDING
// ===========================================================================

/**
 * Runs a program, its output said line by line as the build's.
 *
 * Args:
 *   jobId (number): the build's job.
 *   cmd (string): the program.
 *   args (string[]): its arguments.
 *   env (object): its environment, over this process's.
 *
 * Returns:
 *   Promise<number>: its exit code.
 */
function run(jobId, cmd, args, env = {}) {
  return new Promise((resolve) => {
    const b = builds.get(jobId);
    const child = spawn(cmd, args, { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
    if (b) b.child = child;
    const lines = (stream) => {
      let rest = "";
      return (chunk) => {
        const text = rest + chunk.toString("utf8");
        const parts = text.split(/\r?\n/);
        rest = parts.pop();
        if (parts.length) say(jobId, parts.join("\n"), stream);
      };
    };
    child.stdout.on("data", lines("stdout"));
    child.stderr.on("data", lines("stdout"));
    child.on("error", (e) => {
      say(jobId, `[ERROR]: ${cmd} could not start: ${e.message}`, "stderr");
      resolve(127);
    });
    child.on("close", (code) => {
      if (b) b.child = null;
      resolve(code ?? 1);
    });
  });
}

/**
 * Builds an image and pushes it ; signs it when asked. Ends the build's job.
 *
 * Args:
 *   jobId (number): the build's job.
 *   build (object): { image (its definition), files ({ name: text }), ref, sbom, sign }.
 *   secrets (object): { registry, signing }.
 */
async function runBuild(jobId, build, secrets) {
  const b = builds.get(jobId);
  const dir = fs.mkdtempSync(path.join(workDir(), `build-${jobId}-`));
  let status = "failed";
  let message = "";
  const artifacts = { ref: build.ref, sbom: !!build.sbom, signed: false };
  try {
    // step 1 : the build's files, and the registry's login
    const ctx = path.join(dir, "context");
    fs.mkdirSync(ctx);
    for (const [name, text] of Object.entries(build.files || {})) {
      if (!/^[A-Za-z0-9._-]+$/.test(name)) throw new Error(`'${name}' is no file name of a build`);
      fs.writeFileSync(path.join(ctx, name), String(text));
    }
    const conf = writeDockerConfig(dir, secrets.registry);
    say(jobId, `Building ${build.ref} on builder '${builderName()}'`);
    // step 2 : the build, pushed ; its digest in the metadata file
    const meta = path.join(dir, "metadata.json");
    const args = [
      "--addr", buildkitHost(),
      "build", "--progress=plain",
      "--frontend", "dockerfile.v0",
      "--local", `context=${ctx}`, "--local", `dockerfile=${ctx}`,
      // a registry over plain http (a registry address with http://) : pushed to without TLS
      "--output", `type=image,name=${build.ref},push=true${insecureRegistry(secrets.registry) ? ",registry.insecure=true" : ""}`,
      "--metadata-file", meta,
    ];
    if (build.sbom) args.push("--opt", "attest:sbom=");
    const code = await run(jobId, "buildctl", args, { DOCKER_CONFIG: conf });
    if (b.aborted) {
      status = "aborted";
      message = "the build was aborted";
      return;
    }
    if (code !== 0) {
      message = `the build failed (buildctl exited with ${code})`;
      return;
    }
    const digest = JSON.parse(fs.readFileSync(meta, "utf8"))["containerimage.digest"] || "";
    artifacts.digest = digest;
    say(jobId, `Pushed ${build.ref}${digest ? `@${digest}` : ""}`);
    // step 3 : signed with the team's key, offline (no public transparency log) : cosign 3 signs
    // through a signing config by default, which refuses --tlog-upload=false, so it is turned off
    if (build.sign && secrets.signing?.key) {
      const key = path.join(dir, "cosign.key");
      fs.writeFileSync(key, secrets.signing.key, { mode: 0o600 });
      const target = digest ? `${build.ref.replace(/:[^:/]+$/, "")}@${digest}` : build.ref;
      const signed = await run(jobId, "cosign", ["sign", "--yes", "--use-signing-config=false", "--tlog-upload=false", "--key", key, target], { DOCKER_CONFIG: conf, COSIGN_PASSWORD: secrets.signing.password || "" });
      if (signed !== 0) {
        message = `the image was pushed but could not be signed (cosign exited with ${signed})`;
        return;
      }
      artifacts.signed = true;
      say(jobId, `Signed ${target}`);
    }
    status = "success";
    message = `RTE image ${build.ref} built and pushed`;
  } catch (e) {
    message = `the build failed: ${e.message}`;
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
    b.running = false;
    b.order += 1;
    emit(jobId, { kind: "end", status, order: b.order, stream: status === "success" ? "stdout" : "stderr", message, artifacts });
    sendHealth();
  }
}

// ===========================================================================
// THE CONTROLLER'S MESSAGES
// ===========================================================================

/**
 * A build handed over : claimed (or refused when busy), its secrets opened, run.
 *
 * Args:
 *   msg (object): { jobId, contract, sealed, job: { lastOrder }, build }.
 */
function onRun(msg) {
  const jobId = parseInt(msg.jobId, 10);
  if (msg.contract !== SOCKET_CONTRACT) return session?.send({ t: "refused", jobId, error: `this builder speaks contract ${SOCKET_CONTRACT}` });
  if (!msg.build) return session?.send({ t: "refused", jobId, error: "this is a builder: it builds RTE images, it runs no playbook" });
  if (isStopping()) return session?.send({ t: "refused", jobId, error: "this builder is stopping", busy: true });
  if (builds.get(jobId)?.running) return session?.send({ t: "claimed", jobId });
  if (capacity().free === 0) return session?.send({ t: "refused", jobId, error: "this builder runs a build already", busy: true });
  let secrets;
  try {
    secrets = openJobSecrets(msg.sealed, process.env.RTE_TOKEN, jobId) || {};
  } catch (e) {
    return session?.send({ t: "refused", jobId, error: `the build's secrets do not open: ${e.message}` });
  }
  const spool = new JobSpool(path.join(workDir(), "spool"), jobId);
  const masked = [secrets.registry?.password, secrets.signing?.key, secrets.signing?.password].filter(Boolean);
  builds.set(jobId, { spool, running: true, child: null, aborted: false, order: Number(msg.job?.lastOrder) || 0, secrets: masked });
  session?.send({ t: "claimed", jobId });
  emit(jobId, { kind: "started", pid: process.pid });
  sendHealth();
  runBuild(jobId, msg.build, secrets).catch((e) => logger.error(`Builder: build ${jobId} failed: ${e.message}`));
}

/**
 * A build to stop : its buildctl or cosign killed.
 *
 * Args:
 *   msg (object): { jobId }.
 */
function onCancel(msg) {
  const b = builds.get(parseInt(msg.jobId, 10));
  if (!b?.running) return;
  b.aborted = true;
  b.child?.kill("SIGTERM");
}

/**
 * The controller stored a build's items up to seq : an ended build's spool goes.
 *
 * Args:
 *   msg (object): { jobId, seq }.
 */
function onAck(msg) {
  const jobId = parseInt(msg.jobId, 10);
  const b = builds.get(jobId);
  if (!b) return;
  if (b.spool.ack(Number(msg.seq) || 0) && !b.running) {
    b.spool.remove();
    builds.delete(jobId);
    return;
  }
  pump(jobId);
}

/** the builder's health to the controller : what it builds, its free slot */
export function sendHealth() {
  if (welcomed) session?.send({ t: "health", capacity: capacity(), running: [...builds.entries()].filter(([, b]) => b.running).map(([id]) => id), stopping: isStopping() });
}

/**
 * Takes a socket to the controller : says hello, and once welcomed sends what is not acked yet.
 *
 * Args:
 *   sess (Session): the socket.
 */
export function attach(sess) {
  if (session && session !== sess) session.close(CLOSE.REPLACED, "a newer socket took over");
  session = sess;
  welcomed = false;
  sess.on("m:welcome", (msg) => {
    welcomed = true;
    logger.notice(`Builder '${builderName()}': connected to the controller (node ${msg.node || "?"})`);
    if (msg.pending) logger.warning(`Builder '${builderName()}': waiting for an admin to approve it in the Designer's RTE builders`);
    for (const id of builds.keys()) pump(id);
  });
  sess.on("m:run", onRun);
  sess.on("m:cancel", onCancel);
  sess.on("m:ack", onAck);
  sess.on("close", () => {
    if (session === sess) {
      session = null;
      welcomed = false;
    }
  });
  sess.send({
    t: "hello",
    id: nodeId(),
    name: builderName(),
    version,
    contract: SOCKET_CONTRACT,
    mode: "outbound",
    kind: "builder",
    capacity: capacity(),
    running: [...builds.entries()].filter(([, b]) => b.running).map(([id]) => id),
    spooled: [...builds.keys()],
  });
}

/**
 * The builds an earlier start left unfinished : their spools kept to be sent again, those not
 * ended said abandoned (the builder restarted while it built).
 */
export function resumeSpools() {
  const dir = path.join(workDir(), "spool");
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  for (const file of fs.readdirSync(dir)) {
    const id = parseInt((/^job-(\d+)\.jsonl$/.exec(file) || [])[1], 10);
    if (!id) continue;
    const spool = new JobSpool(dir, id);
    builds.set(id, { spool, running: false, child: null, aborted: false, order: 0, secrets: [] });
    if (!spool.ended) spool.append({ kind: "abandon", message: "the builder restarted while it built" });
  }
}

/** Whether the controller welcomed this builder (its /ready). */
export const isWelcomed = () => welcomed;
