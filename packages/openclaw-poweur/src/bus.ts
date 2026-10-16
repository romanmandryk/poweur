/**
 * The Poweur side of the channel, with no OpenClaw imports: an agent's
 * identity listens on its relay's event stream, picks up its inbox, hands
 * each chat message to `onMessage` and only then consumes it, so a crash
 * while the agent works means the message is picked up again (OpenClaw
 * dedupes on the message id).
 */
import {
  MSG_TYPE_CHAT_TEXT,
  normalizeMessageType,
  streamForever,
  type InboxMessage,
  type PoweurClient,
} from "@poweur/client";

export interface PoweurInbound {
  /** The sender's Poweur ID, verified by its signature. */
  sender: string;
  text: string;
  subject?: string;
  messageId: string;
  threadId?: string;
  /** Milliseconds since the epoch. */
  timestamp: number;
}

export interface PoweurBusOptions {
  client: PoweurClient;
  /** Called once per chat message, in arrival order. A throw leaves the message for the next pickup. */
  onMessage: (message: PoweurInbound) => Promise<void>;
  onError?: (error: unknown, context: string) => void;
  onConnect?: () => void;
  signal?: AbortSignal;
}

export interface PoweurBus {
  /** The agent's own Poweur ID. */
  identity: string;
  /** Send a text message (end-to-end encrypted to `to`); returns the message id. */
  sendText(to: string, text: string, options?: { threadId?: string }): Promise<string>;
  /** Resolves when the stream has stopped (the signal aborted). */
  done: Promise<void>;
}

/** Only ordinary chat reaches the agent; typed and system messages are skipped (and consumed). */
export function toInbound(message: InboxMessage): PoweurInbound | null {
  if (normalizeMessageType(message.type) !== MSG_TYPE_CHAT_TEXT) return null;
  const text = (message.plaintext ?? "").trim();
  if (!text) return null;
  const at = Date.parse(message.timestamp);
  return {
    sender: message.sender.toLowerCase(),
    text,
    ...(message.subject ? { subject: message.subject } : {}),
    messageId: message.id,
    ...(message.thread_id ? { threadId: message.thread_id } : {}),
    timestamp: Number.isFinite(at) ? at : Date.now(),
  };
}

export function startPoweurBus(options: PoweurBusOptions): PoweurBus {
  const { client } = options;
  const report = (error: unknown, context: string) => options.onError?.(error, context);

  // One pickup at a time: two overlapping reads would both see a message that
  // has not been consumed yet.
  let queue: Promise<void> = Promise.resolve();
  const drain = async () => {
    const { messages, cursor } = await client.messages.inbox(client.signer, client.decryptor);
    for (const message of messages) {
      const inbound = toInbound(message);
      if (!inbound) continue;
      await options.onMessage(inbound);
    }
    if (cursor && messages.length > 0) {
      await client.messages.consume(client.signer, { through: cursor, ackThrough: cursor });
    }
  };
  const pickup = () => {
    queue = queue.then(async () => {
      if (options.signal?.aborted) return;
      try {
        await drain();
      } catch (error) {
        report(error, "pickup");
      }
    });
  };

  const done = streamForever(client.relay, client.signer, {
    ...(options.signal ? { signal: options.signal } : {}),
    onOpen: () => {
      options.onConnect?.();
      pickup();
    },
    onEvent: (event) => {
      if (event.type === "ready") return;
      pickup();
    },
    onError: (error) => report(error, "stream"),
  }).then(() => queue);

  return {
    identity: client.identityName,
    async sendText(to, text, sendOptions = {}) {
      const result = await client.send(to, text, sendOptions.threadId ? { threadId: sendOptions.threadId } : {});
      return result.message.id;
    },
    done,
  };
}
