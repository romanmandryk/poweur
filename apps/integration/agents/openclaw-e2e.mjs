#!/usr/bin/env node
// End-to-end test: OpenClaw + the Poweur channel plugin (INT-005-T2) on a real
// local relay.
//
//   node apps/integration/agents/openclaw-e2e.mjs            # fake LLM, no network, CI-safe
//   node apps/integration/agents/openclaw-e2e.mjs --live     # real model via OpenRouter
//
// --live reads OPENROUTER_API_KEY (and OPENROUTER_MODEL) from the environment or
// from apps/integration/agents/.env.local (git-ignored). Needs Go and the built
// SDK (`pnpm --filter @poweur/client build`).
//
// What it proves: a person's Poweur ID messages the agent's Poweur ID, OpenClaw
// runs a turn on it, and the reply comes back as an end-to-end encrypted Poweur
// message. A stranger who is not on the allowlist gets no reply.
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, get as httpGet } from "node:http";
import { createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const ROOT = join(here, "../../..");
const PLUGIN_DIR = join(ROOT, "packages/openclaw-poweur");
const CLI = join(ROOT, "packages/client-ts/dist/cli/bin.js");
const OPENCLAW = join(PLUGIN_DIR, "node_modules/.bin/openclaw");
const live = process.argv.includes("--live");
const keep = process.argv.includes("--keep");
const verbose = process.argv.includes("--verbose");

function loadEnvLocal() {
  const file = join(here, ".env.local");
  if (!existsSync(file)) return {};
  const out = {};
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const m = /^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/.exec(line);
    if (m && m[2]) out[m[1]] = m[2].replace(/^["']|["']$/g, "");
  }
  return out;
}

const freePort = () =>
  new Promise((resolve, reject) => {
    const s = createNetServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close((e) => (e ? reject(e) : resolve(port)));
    });
  });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const children = [];
const work = mkdtempSync(join(tmpdir(), "poweur-openclaw-e2e-"));
function cleanup() {
  for (const c of children) {
    try {
      c.kill("SIGTERM");
    } catch {}
  }
  if (!keep) {
    try {
      rmSync(work, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
    } catch {
      // A child may still be writing as it exits; the OS cleans its temp dir.
    }
  }
}
process.on("exit", cleanup);
process.on("SIGINT", () => process.exit(130));

function start(name, cmd, args, opts) {
  const child = spawn(cmd, args, { ...opts, stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  const take = (d) => {
    log += d.toString();
    if (verbose) process.stderr.write(`[${name}] ${d}`);
  };
  child.stdout.on("data", take);
  child.stderr.on("data", take);
  children.push(child);
  return { child, log: () => log };
}

function run(cmd, args, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { env, stdio: ["ignore", "pipe", "pipe"] });
    let out = "";
    let err = "";
    child.stdout.on("data", (d) => (out += d));
    child.stderr.on("data", (d) => (err += d));
    child.on("close", (code) => (code === 0 ? resolve(out) : reject(new Error(`${cmd} ${args.join(" ")} → ${code}\n${err}${out}`))));
  });
}

async function waitFor(what, fn, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    await sleep(300);
  }
  throw new Error(`timed out waiting for ${what}${last ? `: ${last.message}` : ""}`);
}

// ── A fake OpenAI-compatible model: deterministic answers, streaming or not ──
function startFakeLLM(port) {
  // OpenClaw wraps the person's words in runtime context, so look at all user text.
  const answer = (messages) => {
    const text = messages
      .filter((m) => m.role === "user")
      .map((m) => (typeof m.content === "string" ? m.content : (m.content ?? []).map((p) => p.text ?? "").join(" ")))
      .join("\n");
    if (/\bping\b/i.test(text)) return "pong";
    if (/2\s*\+\s*2/.test(text)) return "4";
    return "ok";
  };
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    if (req.method === "GET" && req.url?.endsWith("/models")) {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({ data: [{ id: "fake-model", object: "model" }] }));
    }
    let request = {};
    try {
      request = JSON.parse(body || "{}");
    } catch {}
    const content = answer(request.messages ?? []);
    const id = `chatcmpl-${Date.now()}`;
    if (request.stream) {
      res.writeHead(200, { "content-type": "text/event-stream" });
      const chunk = (delta, finish = null) =>
        res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", model: "fake-model", choices: [{ index: 0, delta, finish_reason: finish }] })}\n\n`);
      chunk({ role: "assistant", content });
      chunk({}, "stop");
      res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", model: "fake-model", choices: [], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } })}\n\n`);
      return res.end("data: [DONE]\n\n");
    }
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ id, object: "chat.completion", model: "fake-model", choices: [{ index: 0, message: { role: "assistant", content }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
  });
  return new Promise((resolve) => server.listen(port, "127.0.0.1", () => resolve(server)));
}

async function main() {
  if (!existsSync(CLI)) throw new Error("build the SDK first: pnpm --filter @poweur/client build");
  if (!existsSync(OPENCLAW)) throw new Error("install workspace deps first: pnpm install");
  const envLocal = loadEnvLocal();
  const openrouterKey = process.env.OPENROUTER_API_KEY || envLocal.OPENROUTER_API_KEY;
  const openrouterModel = process.env.OPENROUTER_MODEL || envLocal.OPENROUTER_MODEL || "openai/gpt-4o-mini";
  if (live && !openrouterKey) throw new Error("--live needs OPENROUTER_API_KEY (apps/integration/agents/.env.local)");

  // 1. Relay
  const relayPort = await freePort();
  const addr = `127.0.0.1:${relayPort}`;
  const relayUrl = `http://${addr}`;
  const relay = start("relay", "go", ["run", "."], {
    cwd: join(ROOT, "apps/api"),
    env: {
      ...process.env,
      LISTEN_ADDR: `:${relayPort}`, RELAY_ADDRESS: addr, RELAY_SCHEME: "http",
      POWEUR_DATA: join(work, "relay"), HOSTED_DOMAINS: "poweur.net", RESOLVER_ALLOW_PRIVATE: "1",
      RATE_LIMIT_MINUTE: "10000", RATE_LIMIT_HOUR: "100000", RATE_LIMIT_DAY: "1000000",
    },
  });
  await waitFor("relay", async () => (await fetch(`${relayUrl}/health`)).ok, 120_000).catch((e) => {
    throw new Error(`${e.message}\n${relay.log().slice(-2000)}`);
  });
  console.log(`relay up at ${relayUrl}`);

  // 2. A person's Poweur ID and a stranger's. The agent's own ID is created by
  //    `openclaw channels add` below, the way an operator onboards.
  const suffix = Date.now().toString(36);
  const ids = { agent: `agent${suffix}.poweur.net`, alice: `alice${suffix}.poweur.net`, eve: `eve${suffix}.poweur.net` };
  const resolverEnv = { POWEUR_RESOLVER_SCHEME: "http", RESOLVER_ALLOW_PRIVATE: "1", POWEUR_RESOLVER_DIAL: addr };
  const homes = {};
  for (const [who, id] of Object.entries(ids)) {
    homes[who] = join(work, `poweur-${who}`);
    if (who === "agent") continue;
    await run(process.execPath, [CLI, "identity", "create", id, "--hosted", "--relay", relayUrl], {
      ...process.env, ...resolverEnv, POWEUR_HOME: homes[who],
    });
  }
  console.log(`IDs: agent ${ids.agent}, person ${ids.alice}, stranger ${ids.eve}`);
  const poweur = (who, args) => run(process.execPath, [CLI, ...args], { ...process.env, ...resolverEnv, POWEUR_HOME: homes[who] });

  // 3. The model
  const llmPort = await freePort();
  if (!live) await startFakeLLM(llmPort);

  // 4. OpenClaw with the plugin
  const ocHome = join(work, "openclaw");
  const gatewayPort = await freePort();
  const config = {
    gateway: { mode: "local", port: gatewayPort, bind: "loopback", auth: { mode: "token", token: "e2e-gateway-token" } },
    agents: {
      defaults: {
        workspace: join(ocHome, "workspace"),
        model: { primary: live ? `openrouter/${openrouterModel}` : "fake/fake-model" },
      },
    },
    ...(live
      ? {}
      : {
          models: {
            providers: {
              fake: {
                baseUrl: `http://127.0.0.1:${llmPort}/v1`, apiKey: "fake-key", api: "openai-completions",
                models: [{ id: "fake-model", name: "Fake", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32000, maxTokens: 1000 }],
              },
            },
          },
        }),
  };
  const configPath = join(ocHome, "openclaw.json");
  await run("mkdir", ["-p", ocHome]);
  writeFileSync(configPath, JSON.stringify(config, null, 2));
  const ocEnv = {
    ...process.env, ...resolverEnv,
    OPENCLAW_HOME: ocHome, OPENCLAW_STATE_DIR: join(ocHome, "state"), OPENCLAW_CONFIG_PATH: configPath,
    POWEUR_HOME: homes.agent,
    ...(live ? { OPENROUTER_API_KEY: openrouterKey } : {}),
  };
  // Install the plugin the way users get it: the packed npm tarball.
  await run("pnpm", ["--dir", PLUGIN_DIR, "build"], process.env);
  const packDir = join(work, "pack");
  await run("mkdir", ["-p", packDir]);
  await run("pnpm", ["--dir", PLUGIN_DIR, "pack", "--pack-destination", packDir], process.env);
  const tarball = join(packDir, readdirSync(packDir).find((f) => f.endsWith(".tgz")));
  await run(OPENCLAW, ["plugins", "install", `npm-pack:${tarball}`, "--accept-capabilities", "--force"], ocEnv);
  console.log("plugin installed from the packed tarball");

  // Onboarding: one command creates the agent's ID, configures the channel and labels its profile.
  const added = await run(OPENCLAW, ["channels", "add", "--channel", "poweur", "--identity", ids.agent, "--allow-from", ids.alice, "--relay-url", relayUrl], ocEnv);
  if (!/Created the agent's Poweur ID/.test(added) || !/recovery phrase/i.test(added)) throw new Error(`channels add said:\n${added}`);
  const written = JSON.parse(readFileSync(configPath, "utf8")).channels?.poweur ?? {};
  if (written.identity !== ids.agent || written.dmPolicy !== "allowlist" || !written.allowFrom?.includes(ids.alice)) {
    throw new Error(`channels add wrote ${JSON.stringify(written)}`);
  }
  const profile = await new Promise((resolve) => {
    httpGet({ host: "127.0.0.1", port: relayPort, path: "/.well-known/poweur/profile.json", headers: { host: ids.agent } }, (res) => {
      let body = "";
      res.on("data", (d) => (body += d));
      res.on("end", () => {
        try {
          resolve(JSON.parse(body));
        } catch {
          resolve(null);
        }
      });
    }).on("error", () => resolve(null));
  });
  if (!/run by/.test(profile?.bio ?? "")) throw new Error(`agent profile not labelled: ${JSON.stringify(profile)}`);
  console.log(`channels add: created ${ids.agent}, allowlist [${written.allowFrom}], profile "${profile?.bio ?? "?"}"`);

  const gateway = start("openclaw", OPENCLAW, ["gateway", "run", "--allow-unconfigured"], { cwd: work, env: ocEnv });
  await waitFor("the Poweur channel to start", async () => /Poweur channel as|starting Poweur channel/.test(gateway.log()), 120_000).catch((e) => {
    throw new Error(`${e.message}\n--- openclaw log ---\n${gateway.log().slice(-4000)}`);
  });
  console.log("OpenClaw gateway up, Poweur channel started");

  // 5. Conversation. Reading the inbox picks messages up, so every read is
  // collected: that is how a turn's message count is measured.
  const fromAgent = { alice: [], eve: [] };
  const pickUp = async (who) => {
    const msgs = (JSON.parse(await poweur(who, ["inbox", "--json"])).messages ?? []).filter((m) => m.sender === ids.agent);
    fromAgent[who].push(...msgs);
    return msgs;
  };
  const ask = async (who, text, expect, timeoutMs) => {
    const before = fromAgent[who].length;
    await poweur(who, ["send", ids.agent, text]);
    await waitFor(`a reply to "${text}"`, async () => (await pickUp(who)).length > 0, timeoutMs).catch((e) => {
      throw new Error(`${e.message}\n--- openclaw log ---\n${gateway.log().slice(-4000)}`);
    });
    // Anything else the same turn sends (split replies, progress) arrives shortly after.
    await sleep(live ? 6000 : 2500);
    await pickUp(who);
    const turn = fromAgent[who].slice(before);
    const reply = turn.map((m) => m.plaintext ?? "").join("\n");
    console.log(`${who} → agent: ${text}\nagent → ${who}: ${reply.length > 300 ? reply.slice(0, 300) + "…" : reply}\n(messages this turn: ${turn.length})`);
    if (expect && !expect.test(reply)) throw new Error(`unexpected reply: ${reply}`);
    if (turn.length > 3) throw new Error(`one turn sent ${turn.length} messages; replies should not be split this much`);
    return reply;
  };

  if (live) {
    await ask("alice", "What is 2+2? Reply with just the number.", /\b4\b/, 120_000);
    await ask("alice", "Give me five short tips for writing a good README, as a numbered list.", /1/, 180_000);
  } else {
    await ask("alice", "ping", /pong/, 60_000);
  }

  // 6. A shared folder: alice shares "Project" with the agent (write). It
  // appears in the agent's workspace; what the agent writes there lands in
  // alice's drive, and alice's edits come down.
  const workspace = join(ocHome, "workspace");
  const mirror = join(workspace, "shared", ids.alice, "Project");
  const briefFile = join(work, "brief.md");
  writeFileSync(briefFile, "# Brief\nPlan a 3-day trip to Lisbon for two people in May. Budget: 1200 EUR.\n");
  await poweur("alice", ["drive", "mkdir", "/Project"]);
  await poweur("alice", ["drive", "put", briefFile, "/Project/brief.md"]);
  await poweur("alice", ["drive", "share", "add", "/Project", ids.agent, "--role", "write"]);
  await waitFor("the agent to accept the folder", async () => (await pickUp("alice")).some((m) => /Got your folder "Project"/.test(m.plaintext ?? "")), 60_000).catch((e) => {
    throw new Error(`${e.message}\n--- openclaw log ---\n${gateway.log().slice(-4000)}`);
  });
  await waitFor("brief.md in the agent's workspace", async () => existsSync(join(mirror, "brief.md")), 30_000);
  console.log(`folder shared: brief.md is in the agent's workspace (${join("shared", ids.alice, "Project")})`);
  const projectFiles = async () => JSON.parse(await poweur("alice", ["drive", "list", "/Project", "--json"])).map((f) => f.name);
  const readProject = async (name) => {
    const out = join(work, `got-${Date.now()}-${name}`);
    await poweur("alice", ["drive", "get", `/Project/${name}`, out]);
    return readFileSync(out, "utf8");
  };

  if (live) {
    await ask("alice", "In the shared folder Project, read brief.md and write a short plan (at most 10 lines) to plan.md in the same folder. Then reply with just: done", null, 240_000);
    const names = await waitFor("plan.md in alice's drive", async () => ((await projectFiles()).includes("plan.md") ? true : false), 90_000);
    const plan = await readProject("plan.md");
    console.log(`the agent wrote plan.md into alice's folder:\n${plan.slice(0, 400)}`);
    if (!/lisbon/i.test(plan)) throw new Error("plan.md does not look like a plan for the brief");
  } else {
    // The agent's work, as its file tools would leave it.
    writeFileSync(join(mirror, "notes.md"), "Notes from the agent\n");
    await ask("alice", "ping", /pong/, 60_000);
    await waitFor("notes.md in alice's drive", async () => (await projectFiles()).includes("notes.md"), 30_000);
    if ((await readProject("notes.md")) !== "Notes from the agent\n") throw new Error("notes.md arrived with the wrong content");
    console.log("the agent's notes.md is in alice's folder");
    // alice edits the brief; the next turn brings it down.
    writeFileSync(briefFile, "# Brief v2\nNow 4 days.\n");
    await poweur("alice", ["drive", "put", briefFile, "/Project/brief.md"]);
    await ask("alice", "ping", /pong/, 60_000);
    if (!readFileSync(join(mirror, "brief.md"), "utf8").includes("v2")) throw new Error("alice's edit did not reach the agent");
    console.log("alice's edit to brief.md reached the agent's workspace");
  }

  // A stranger is not on the allowlist: no reply.
  await poweur("eve", ["send", ids.agent, "ping"]);
  await sleep(live ? 8000 : 4000);
  if ((await pickUp("eve")).length > 0) throw new Error("the agent answered a stranger");
  console.log("stranger got no reply (allowlist holds)");
  console.log("PASS");
}

main().then(
  () => process.exit(0),
  (e) => {
    console.error(`FAIL: ${e.message}`);
    if (keep) console.error(`work dir kept: ${work}`);
    process.exit(1);
  },
);
