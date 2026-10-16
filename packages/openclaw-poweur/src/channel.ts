/**
 * The Poweur channel for OpenClaw (INT-005-T2): the agent runs on its own
 * Poweur ID, and people message it from the Poweur app, the PWA or the CLI.
 * Messages are end-to-end encrypted and every sender is a verified Poweur ID,
 * so the allowlist and pairing work on real identities, not display names.
 *
 * Shaped like OpenClaw's bundled Nostr channel (a long-lived relay stream
 * rather than a webhook): the transport is ./bus.ts, the rest is OpenClaw's
 * shared DM-security, pairing, inbound dispatch and outbound plumbing.
 */
import { openClient } from "@poweur/client/node";
import {
  createScopedDmSecurityResolver,
  createTopLevelChannelConfigAdapter,
} from "openclaw/plugin-sdk/channel-config-helpers";
import type {
  ChannelGatewayContext,
  ChannelOutboundAdapter,
} from "openclaw/plugin-sdk/channel-contract";
import { createChatChannelPlugin } from "openclaw/plugin-sdk/channel-core";
import { missingTargetError } from "openclaw/plugin-sdk/channel-feedback";
import type { StableChannelIngressIdentityParams } from "openclaw/plugin-sdk/channel-ingress-runtime";
import {
  createChannelMessageAdapterFromOutbound,
  runPassiveAccountLifecycle,
} from "openclaw/plugin-sdk/channel-outbound";
import { createChannelPairingController } from "openclaw/plugin-sdk/channel-pairing";
import {
  buildChannelConfigSchema,
  DEFAULT_ACCOUNT_ID,
  formatPairingApproveHint,
  type ChannelPlugin,
} from "openclaw/plugin-sdk/channel-plugin-common";
import { attachChannelToResult } from "openclaw/plugin-sdk/channel-send-result";
import type { OpenClawConfig } from "openclaw/plugin-sdk/config-contracts";
import { buildChannelOutboundSessionRoute, type ChannelOutboundSessionRouteParams } from "openclaw/plugin-sdk/core";
import { channelReadyPatch } from "openclaw/plugin-sdk/gateway-runtime";
import type { PluginRuntime } from "openclaw/plugin-sdk/plugin-runtime";
import {
  collectStatusIssuesFromLastError,
  createComputedAccountStatusAdapter,
  createDefaultChannelRuntimeState,
} from "openclaw/plugin-sdk/status-helpers";
import { chunkTextForOutbound, sanitizeAssistantVisibleText, stripMarkdown } from "openclaw/plugin-sdk/text-chunking";
import { startPoweurBus, type PoweurBus } from "./bus.js";
import {
  looksLikePoweurId,
  normalizePoweurId,
  PoweurConfigSchema,
  resolvePoweurAccount,
  type ResolvedPoweurAccount,
} from "./config.js";
import { getPoweurRuntime } from "./runtime.js";

const CHANNEL = "poweur";
const TARGET_HINT = "<poweur id, e.g. alice.poweur.net>";

const activeBuses = new Map<string, PoweurBus>();

function normalizeAllowEntry(entry: string): string | null {
  const trimmed = entry.trim();
  if (!trimmed) return null;
  if (trimmed === "*") return "*";
  return looksLikePoweurId(trimmed) ? normalizePoweurId(trimmed) : null;
}

const poweurIngressIdentity = {
  key: "poweur-id",
  normalizeEntry: normalizeAllowEntry,
  normalizeSubject: (value: string) => (looksLikePoweurId(value) ? normalizePoweurId(value) : null),
  sensitivity: "pii",
  entryIdPrefix: "poweur-entry",
} satisfies StableChannelIngressIdentityParams;

function resolveTargetId(to: string | undefined): string | null {
  const trimmed = (to ?? "").trim();
  return trimmed && looksLikePoweurId(trimmed) ? normalizePoweurId(trimmed) : null;
}

/** Plain text for the Poweur app: tool traces removed, Markdown flattened. */
function toPoweurText(cfg: OpenClawConfig, accountId: string, text: string): string {
  const runtime = getPoweurRuntime();
  const sanitized = sanitizeAssistantVisibleText(text);
  if (!sanitized) return "";
  const tableMode = runtime.channel.text.resolveMarkdownTableMode({ cfg, channel: CHANNEL, accountId });
  return stripMarkdown(runtime.channel.text.convertMarkdownTables(sanitized, tableMode));
}

export async function startPoweurGatewayAccount(ctx: ChannelGatewayContext<ResolvedPoweurAccount>): Promise<void> {
  const account = ctx.account;
  ctx.setStatus({ accountId: account.accountId, lifecycle: "starting" });
  if (!account.configured) throw new Error("Poweur: set channels.poweur.identity to the agent's Poweur ID");

  const channelRuntime = ctx.channelRuntime as PluginRuntime["channel"] | undefined;
  if (!channelRuntime?.inbound?.buildContext) {
    throw new Error("Poweur requires its registered channel runtime context builder");
  }
  const runtime = getPoweurRuntime();
  const { client } = await openClient({
    identity: account.identity,
    ...(account.relayUrl ? { relayUrl: account.relayUrl } : {}),
  });
  ctx.log?.info?.(`[${account.accountId}] starting Poweur channel as ${account.identity}`);

  const pairing = createChannelPairingController({ core: runtime, channel: CHANNEL, accountId: account.accountId });
  const resolveInboundAccess = async (
    sender: string,
    rawBody: string,
    contextBinding?: import("openclaw/plugin-sdk/channel-ingress-runtime").ChannelIngressContextBinding,
  ) =>
    await channelRuntime.inbound.ingress.resolveStable({
      channelId: CHANNEL,
      accountId: account.accountId,
      identity: poweurIngressIdentity,
      cfg: ctx.cfg,
      useDefaultPairingStore: true,
      subject: { stableId: sender },
      conversation: { kind: "direct", id: sender },
      contextBinding,
      dmPolicy: account.config.dmPolicy ?? "pairing",
      allowFrom: account.config.allowFrom,
      command: runtime.channel.commands.shouldComputeCommandAuthorized(rawBody, ctx.cfg)
        ? { modeWhenAccessGroupsOff: "configured" }
        : undefined,
    });

  await runPassiveAccountLifecycle({
    abortSignal: ctx.abortSignal,
    start: async () => {
      const controller = new AbortController();
      const bus = startPoweurBus({
        client,
        signal: controller.signal,
        onConnect: () => ctx.setStatus(channelReadyPatch({ accountId: account.accountId })),
        onError: (error, where) => ctx.log?.warn?.(`[${account.accountId}] Poweur ${where}: ${String(error)}`),
        onMessage: async (message) => {
          const reply = async (text: string) => {
            await bus.sendText(message.sender, text, message.threadId ? { threadId: message.threadId } : {});
          };
          const access = await resolveInboundAccess(message.sender, message.text);
          if (access.senderAccess.decision === "pairing") {
            await pairing.issueChallenge({
              senderId: message.sender,
              senderIdLine: `Your Poweur ID: ${message.sender}`,
              sendPairingReply: reply,
              onReplyError: (err) => ctx.log?.warn?.(`[${account.accountId}] pairing reply to ${message.sender} failed: ${String(err)}`),
            });
            return;
          }
          if (access.senderAccess.decision !== "allow") {
            ctx.log?.debug?.(`[${account.accountId}] blocked Poweur sender ${message.sender} (${access.senderAccess.reasonCode})`);
            return;
          }
          const text = message.subject ? `${message.subject}\n\n${message.text}` : message.text;
          const { dispatchInboundDirectDm } = await import("openclaw/plugin-sdk/channel-inbound");
          await dispatchInboundDirectDm({
            channelRuntime,
            resolveChannelIngress: async (contextBinding) => {
              const exact = await resolveInboundAccess(message.sender, text, contextBinding);
              if (!exact.senderAccess.allowed) {
                throw new Error(`Poweur sender authorization changed before dispatch (${message.sender})`);
              }
              return exact;
            },
            cfg: ctx.cfg,
            channel: CHANNEL,
            channelLabel: "Poweur",
            accountId: account.accountId,
            peer: { kind: "direct", id: message.sender },
            senderId: message.sender,
            senderAddress: `poweur:${message.sender}`,
            recipientAddress: `poweur:${account.identity}`,
            conversationLabel: message.sender,
            rawBody: text,
            messageId: message.messageId,
            timestamp: message.timestamp,
            commandAuthorized: access.commandAccess.requested ? access.commandAccess.authorized : undefined,
            deliver: async (payload) => {
              const out = toPoweurText(ctx.cfg, account.accountId, payload.text ?? "");
              if (out) await reply(out);
            },
            onRecordError: (err) => ctx.log?.error?.(`[${account.accountId}] failed recording Poweur inbound session: ${String(err)}`),
            onDispatchError: (err, info) => ctx.log?.error?.(`[${account.accountId}] Poweur ${info.kind} reply failed: ${String(err)}`),
          });
        },
      });
      activeBuses.set(account.accountId, bus);
      return {
        stop: async () => {
          if (activeBuses.get(account.accountId) === bus) activeBuses.delete(account.accountId);
          controller.abort();
          await bus.done.catch(() => undefined);
          ctx.log?.info?.(`[${account.accountId}] Poweur channel stopped`);
        },
      };
    },
    stop: async (monitor) => {
      await monitor.stop();
    },
  });
}

function busFor(cfg: OpenClawConfig, accountId?: string | null): { bus: PoweurBus; accountId: string } {
  const aid = accountId ?? DEFAULT_ACCOUNT_ID;
  const bus = activeBuses.get(aid);
  if (!bus) throw new Error(`Poweur channel is not running for account ${aid}`);
  return { bus, accountId: aid };
}

export const poweurOutboundAdapter = {
  deliveryMode: "direct",
  textChunkLimit: 8000,
  chunker: chunkTextForOutbound,
  sanitizeText: ({ text }) => sanitizeAssistantVisibleText(text),
  deliveryCapabilities: { durableFinal: { text: true, messageSendingHooks: true } },
  resolveTarget: ({ to }) => {
    const id = resolveTargetId(to);
    return id ? { ok: true, to: id } : { ok: false, error: missingTargetError("Poweur", TARGET_HINT) };
  },
  sendText: async ({ cfg, to, text, accountId }) => {
    const { bus, accountId: aid } = busFor(cfg, accountId);
    const target = resolveTargetId(to);
    if (!target) throw new Error(`Poweur target must be a Poweur ID like alice.poweur.net (got "${to}")`);
    const message = toPoweurText(cfg, aid, text ?? "");
    if (!message) throw new Error("Poweur send requires non-empty text");
    const messageId = await bus.sendText(target, message);
    return attachChannelToResult(CHANNEL, { to: target, messageId });
  },
} satisfies ChannelOutboundAdapter;

export const poweurPairingTextAdapter = {
  idLabel: "poweurId",
  message: "Your pairing request has been approved!",
  normalizeAllowEntry: (entry: string) => normalizeAllowEntry(entry) ?? entry.trim(),
  notify: async ({ cfg, id, message, accountId }: { cfg: OpenClawConfig; id: string; message: string; accountId?: string }) => {
    const bus = activeBuses.get(accountId ?? DEFAULT_ACCOUNT_ID);
    if (bus) await bus.sendText(normalizePoweurId(id), message);
    void cfg;
  },
};

function resolveOutboundSessionRoute(params: ChannelOutboundSessionRouteParams) {
  const target = resolveTargetId(params.target.replace(/^poweur:/i, ""));
  if (!target) return null;
  return buildChannelOutboundSessionRoute({
    cfg: params.cfg,
    agentId: params.agentId,
    channel: CHANNEL,
    accountId: params.accountId,
    recipientSessionExact: true,
    peer: { kind: "direct", id: target },
    chatType: "direct",
    from: `poweur:${target}`,
    to: `poweur:${target}`,
  });
}

const resolveDmPolicy = createScopedDmSecurityResolver<ResolvedPoweurAccount>({
  channelKey: CHANNEL,
  resolvePolicy: (account) => account.config.dmPolicy,
  resolveAllowFrom: (account) => account.config.allowFrom,
  policyPathSuffix: "dmPolicy",
  defaultPolicy: "pairing",
  approveHint: formatPairingApproveHint(CHANNEL),
  normalizeEntry: (entry) => normalizeAllowEntry(entry) ?? entry.trim(),
});

const configAdapter = createTopLevelChannelConfigAdapter<ResolvedPoweurAccount>({
  sectionKey: CHANNEL,
  resolveAccount: (cfg) => resolvePoweurAccount(cfg),
  listAccountIds: (cfg) => (resolvePoweurAccount(cfg).configured ? [DEFAULT_ACCOUNT_ID] : []),
  defaultAccountId: () => DEFAULT_ACCOUNT_ID,
  deleteMode: "clear-fields",
  clearBaseFields: ["name", "identity", "relayUrl", "dmPolicy", "allowFrom"],
  resolveAllowFrom: (account) => account.config.allowFrom,
  formatAllowFrom: (allowFrom) =>
    allowFrom.map((entry) => normalizeAllowEntry(String(entry))).filter((entry): entry is string => Boolean(entry)),
});

export const poweurPlugin: ChannelPlugin<ResolvedPoweurAccount> = createChatChannelPlugin({
  base: {
    id: CHANNEL,
    meta: {
      id: CHANNEL,
      label: "Poweur",
      selectionLabel: "Poweur ID",
      docsPath: "https://www.poweur.org/docs/",
      docsLabel: "poweur",
      blurb: "End-to-end encrypted messages to and from Poweur IDs; the agent has its own ID.",
      order: 110,
    },
    capabilities: { chatTypes: ["direct"], media: false },
    reload: { configPrefixes: ["channels.poweur"] },
    configSchema: buildChannelConfigSchema(PoweurConfigSchema),
    config: {
      ...configAdapter,
      isConfigured: (account) => account.configured,
      inspectAccount: (cfg) => {
        const account = resolvePoweurAccount(cfg);
        return { enabled: account.enabled, configured: account.configured };
      },
    },
    messaging: {
      targetPrefixes: [CHANNEL],
      normalizeTarget: (target) => resolveTargetId(target.replace(/^poweur:/i, "")) ?? target.trim(),
      inferTargetChatType: ({ to }) => (resolveTargetId(to.replace(/^poweur:/i, "")) ? "direct" : undefined),
      targetResolver: {
        looksLikeId: (input) => looksLikePoweurId(input.replace(/^poweur:/i, "")),
        hint: TARGET_HINT,
      },
      resolveOutboundSessionRoute,
    },
    message: createChannelMessageAdapterFromOutbound({ id: CHANNEL, outbound: poweurOutboundAdapter }),
    status: createComputedAccountStatusAdapter<ResolvedPoweurAccount>({
      defaultRuntime: createDefaultChannelRuntimeState(DEFAULT_ACCOUNT_ID),
      collectStatusIssues: (accounts) => collectStatusIssuesFromLastError(CHANNEL, accounts),
      resolveAccountSnapshot: ({ account }) => ({
        accountId: account.accountId,
        name: account.name,
        enabled: account.enabled,
        configured: account.configured,
        extra: { identity: account.identity },
      }),
    }),
    gateway: { startAccount: startPoweurGatewayAccount },
  },
  pairing: { text: poweurPairingTextAdapter },
  security: { resolveDmPolicy },
  outbound: poweurOutboundAdapter,
});
