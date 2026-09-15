# INT-003 — Open-source AI tools & agent framework integrations

- **Status:** proposed
- **Poweur prerequisites:** EPIC-004 (changes feed), EPIC-005 (shared folders), EPIC-008 (scoped grants), EPIC-009 (typed messages, push), EPIC-010 (agent SDK), [EPIC-017](../EPIC-017-typescript-client-sdk.md) (`@poweur/client` — the TS/JS deliverables below build on it)
- **Sibling:** [INT-005](INT-005-agent-control-planes.md) covers personal **agent control planes** (OpenClaw, Hermes), whose integration shape is a channel plugin rather than a toolkit
- **Goal:** make Poweur the **collaboration fabric for AI**: agents and assistants get real,
  verifiable identities; users hand work to agents (their own or other people's) by sharing a
  folder or sending a message; results land back in the requester's home. The pattern every
  integration below implements is the same: *intermediate artifacts (conversations, tasks,
  files) live in shared Poweur folders where any authorized agent — from any vendor's tool —
  can pick them up and deliver results back.* That interop story is something none of these
  projects can build alone, because it requires a neutral identity + storage layer between them.

---

## A. Highest leverage: protocol-level

### Model Context Protocol (MCP) — `modelcontextprotocol`
MCP is becoming the USB port of AI assistants. A **Poweur MCP server** exposing
`send_message`, `read_inbox`, `list_shared_folder`, `read_file`, `write_file`, `create_share`
instantly gives *every* MCP-capable client (Claude, IDEs, a fast-growing list of open-source
hosts) hands on the Poweur substrate — no per-tool integration. The reverse direction matters
too: Poweur scoped tokens (E08-T4) are a clean answer to MCP's still-open question of how a
server safely accesses *a specific user's* data. This is the first integration to build: one
artifact, the whole assistant ecosystem.

### LangChain — `langchain-ai/langchain` & LlamaIndex — `run-llama/llama_index`
The default toolkits for building LLM apps in Python/JS. Contribution: a `PoweurToolkit`
(tools for messaging, file read/write, share management) and document loaders/readers that
ingest from a Poweur home or shared folder. For their ecosystems this adds something missing
from every toolkit catalog: a *trusted inter-agent and agent-to-human channel* with built-in
identity — today their agents can call APIs but have no native way to be *addressed*, paged, or
handed work by an outside party.

---

## B. Chat UIs & assistant front-ends (T2 + T3 + T4)

### Open WebUI — `open-webui/open-webui`
The most popular self-hosted LLM chat UI. Integration in three steps: Poweur sign-in (they
already support OIDC — works with EPIC-022 immediately); chat history stored under
`/apps/com.openwebui/` in the *user's* home so conversations survive reinstalls and roam
between instances; and **conversation sharing to a Poweur ID** — share a chat folder with a
colleague or with an agent that continues the work overnight and writes results back. Open WebUI
gets the feature users keep asking every chat UI for — portable history and real collaboration —
without building accounts, sync or sharing infrastructure themselves.

### LibreChat — `danny-avila/LibreChat`
The multi-provider chat UI; same three-step pattern as Open WebUI. LibreChat's differentiator
is being provider-agnostic on the *model* side; Poweur makes it provider-agnostic on the
*data* side — your conversations belong to your identity, not to the instance you happened to
use. That's a clean ideological extension of their pitch, which makes a persuasive PR.

### Ollama — `ollama/ollama`
The local-LLM runtime. Light-touch integration: a documented recipe + small wrapper making any
Ollama-served model addressable as `assistant.alice.poweur.net` — your personal, E2E-encrypted
AI you can message from your phone, that can only be reached by your contacts. For Ollama this
showcases exactly the private-AI story they exist for: local model, encrypted transport,
zero cloud.

---

## C. Agent frameworks & autonomous coders (T4 — shared-folder work handoff)

### CrewAI — `crewAIInc/crewAI`
Orchestrates "crews" of role-based agents — currently all inside one process owned by one
party. Poweur identities per agent + shared task folders let crews span **organizations**: your
research agent and my writing agent collaborate through a folder both can see, every
contribution signed and attributable. Cross-org crews are an enterprise feature CrewAI cannot
deliver without exactly this kind of neutral identity/transport layer.

### AutoGen / AG2 — `microsoft/autogen`, `ag2ai/ag2`
Multi-agent conversation frameworks built on in-process message passing. Contribution: a
transport adapter mapping their conversational messages onto Poweur typed messages, so agent
teams become distributed, persistent (relay spools while an agent is offline) and auditable.
Their abstractions stay untouched — it's "the same conversation, but across the internet with
verified participants."

### OpenHands — `All-Hands-AI/OpenHands`
The leading open autonomous coding agent. Integration: OpenHands watches a shared
`/apps/dev.openhands/inbox/` folder for task files (per the EPIC-006 tasks convention),
executes, and delivers patches/artifacts + a signed summary message back to the requester's
home. That turns OpenHands from a tool you sit in front of into a **service colleagues can
delegate to** — and the operator-attestation chain (E10-T1) answers "whose code agent touched
this?" for compliance-minded teams.

### AutoGPT — `Significant-Gravitas/AutoGPT`
One of the most-starred agent projects, now a platform for continuous agents. Poweur gives its
agents the two things its marketplace model needs: verifiable identity for published agents
(operator chain, INT/EPIC-010-T5 directory) and a standard delivery channel for results.

### Aider — `Aider-AI/aider` (and similar CLI coding agents)
Terminal-first pair programmer. Minimal integration with outsized demo value: an `--inbox`
mode where aider processes change-requests arriving as Poweur messages on a repo it has
checked out — code review comments from your phone, by messaging your own bot.

---

## D. Visual builders & workflow automation (T4 — trigger/action nodes)

For all of these, the contribution is a **Poweur node/plugin pack**: triggers (message
received, file changed in shared folder, share offered) and actions (send message, write file,
create share). The pitch is identical and strong: their users currently glue workflows to
email/Slack/Drive — closed, spam-prone, OAuth-heavy endpoints. Poweur endpoints are
self-hostable, E2E-encrypted, identity-verified and free.

### n8n — `n8n-io/n8n`
The dominant fair-code workflow automation tool with a huge node marketplace. A Poweur
community node makes every n8n instance a Poweur automation runner — and because n8n is what
much of the "AI agents for business" crowd actually deploys, it shortcuts EPIC-010's rules
engine for power users on day one.

### Node-RED — `node-red/node-red`
The OpenJS Foundation's flow tool, ubiquitous in IoT/home automation. Poweur nodes give devices
and flows a verified identity and an encrypted message channel that traverses NAT via relays —
solving the remote-access problem that today pushes Node-RED users to third-party MQTT brokers.

### Activepieces — `activepieces/activepieces` & Windmill — `windmill-labs/windmill` & Huginn — `huginn/huginn`
The open Zapier-alternatives; each has a piece/script/agent plugin model where a Poweur
package slots in cleanly. Land n8n and Node-RED first, then port the pack — same core, three
more ecosystems.

### Dify — `langgenius/dify` & Flowise — `FlowiseAI/Flowise` & Langflow — `langflow-ai/langflow`
Visual LLM-app builders, hugely popular for prototyping agents. A Poweur tool-block means
every app built in them can be published *as* a messageable Poweur identity — "deploy your
Dify bot to a name your users can message and share files with" is a distribution channel none
of them currently offers their builders.

### Home Assistant — `home-assistant/core`
The largest open-source home automation project. A Poweur integration delivers notifications
over an encrypted, self-hosted channel (today's options: paid cloud or DIY push plumbing) and
lets automations accept commands only from household-contact IDs — verified family access
without exposing the instance to the internet. Their community's privacy ethos makes this an
easy cultural fit.

---

## Tasks

- [ ] **INT-003-T1** Poweur MCP server (reference implementation, ships with the agent SDK; built on `@poweur/client`, EPIC-017)
- [ ] **INT-003-T2** LangChain toolkit + LlamaIndex reader, published to PyPI/npm with docs PRs upstream
- [ ] **INT-003-T3** Open WebUI: OIDC sign-in tutorial → storage backend PR → conversation-share PoC
- [ ] **INT-003-T4** n8n community node pack (triggers + actions), then Node-RED port — both on `@poweur/client` (EPIC-017)
- [ ] **INT-003-T5** OpenHands shared-folder task pickup PoC (the flagship demo: delegate a
      coding task to a colleague's agent, get a patch back)
- [ ] **INT-003-T6** CrewAI/AutoGen transport adapters PoC (cross-org crew demo)
- [ ] **INT-003-T7** Ollama personal-assistant recipe + LibreChat parity with INT-003-T3
- [ ] **INT-003-T8** Home Assistant notification integration PoC
