import { useEffect, useRef, useState, type ReactNode } from "react";
import { sendAnonymous } from "@poweur/client";
import { resolveOptionsForRelay } from "../lib/client.js";
import { defaultRelayUrl, identityOriginUrl } from "../lib/storage.js";
import { useSession } from "../state/session";
import { BrandMark } from "../ui/Logo";
import { Button } from "../ui/Button";
import { FormGroup, Label, Note, Textarea } from "../ui/Field";

export function isPublicAnonymousRoute(search = globalThis.location?.search ?? ""): boolean {
  return new URLSearchParams(search).get("anonymous") === "1";
}

/** A stale public advertisement must not surface inbox policy, limits, or challenge settings. */
export function publicAnonymousFailure(error: unknown): string {
  const detail = typeof error === "object" && error && "detail" in error ? String((error as { detail?: unknown }).detail ?? "") : "";
  const message = error instanceof Error ? error.message : "";
  const blob = `${detail}\n${message}`;
  if (blob.includes("policy_rejected") || blob.includes("does not accept anonymous")) {
    return "This identity is not accepting anonymous messages.";
  }
  if (blob.includes("rate_limit_exceeded")) {
    return "Too many anonymous messages were sent from here. Try again later.";
  }
  if (blob.includes("anon_too_large")) {
    return "That message is too long to send.";
  }
  return "The message could not be sent.";
}

export function PublicAnonymousComposer() {
  const mode = useSession((state) => state.mode);
  const recipient = mode.mode === "identity" ? (mode.subject ?? "") : "";
  const [body, setBody] = useState("");
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState(false);
  const [status, setStatus] = useState("");
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []);

  async function submit() {
    const message = body.trim();
    if (!recipient || !message || sending) return;
    // This is a recipient-hosted public route. Keep the current environment's
    // scheme/port but do not send through an unrelated stored identity's host.
    const relayUrl = identityOriginUrl(recipient, defaultRelayUrl());
    if (!relayUrl) {
      setStatus("This identity's relay is unavailable.");
      return;
    }
    abort.current?.abort();
    abort.current = new AbortController();
    setSending(true);
    setStatus("Encrypting your message…");
    try {
      await sendAnonymous(recipient, message, {
        targetRelayUrl: relayUrl,
        resolve: resolveOptionsForRelay(relayUrl),
        scheme: new URL(relayUrl).protocol === "http:" ? "http" : "https",
        signal: abort.current.signal,
        onChallenge: ({ bits }) => setStatus(`This inbox requires proof of work (${bits} bits)…`),
        onSolveProgress: (attempts) => setStatus(`Working… ${attempts.toLocaleString()} attempts`),
      });
      setSent(true);
      setBody("");
      setStatus("Message sent anonymously.");
    } catch (error) {
      if ((error as Error).name === "AbortError") return;
      setStatus(publicAnonymousFailure(error));
    } finally {
      setSending(false);
      abort.current = null;
    }
  }

  if (!mode.probed) {
    return <PublicComposerFrame><p className="text-muted">Loading identity…</p></PublicComposerFrame>;
  }
  if (!recipient) {
    return (
      <PublicComposerFrame>
        <h1 className="text-2xl font-extrabold">Anonymous messaging unavailable</h1>
        <p className="mt-3 text-muted">This address is not a Poweur identity page.</p>
        <a className="mt-6 text-accent" href="/">Return to the site</a>
      </PublicComposerFrame>
    );
  }

  return (
    <PublicComposerFrame>
      <p className="text-xs font-bold tracking-[.12em] text-accent uppercase">Anonymous message</p>
      <h1 className="mt-2 text-2xl font-extrabold tracking-tight">Write to {recipient}</h1>
      <p className="mt-3 text-sm leading-relaxed text-muted">
        Your message is encrypted for this identity but is not signed. They cannot verify who sent it.
      </p>
      <FormGroup className="mt-6 text-left">
        <Label htmlFor="public-anon-body">Message</Label>
        <Textarea
          id="public-anon-body"
          value={body}
          maxLength={4096}
          disabled={sending || sent}
          placeholder="Write a message"
          onChange={(event) => setBody(event.currentTarget.value)}
        />
      </FormGroup>
      {!sent && (
        <Button id="public-anon-send" disabled={sending || !body.trim()} onClick={() => void submit()}>
          {sending ? "Sending…" : "Send anonymously"}
        </Button>
      )}
      <p id="public-anon-status" className="mt-4 min-h-6 text-sm text-muted" role="status" aria-live="polite">
        {status}
      </p>
      <Note>This page never asks for or uses a Poweur identity stored in this browser.</Note>
      <a className="mt-5 inline-block text-sm text-accent" href="/">Back to {recipient}</a>
    </PublicComposerFrame>
  );
}

function PublicComposerFrame({ children }: { children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-bg p-4">
      <section className="w-full max-w-[480px] rounded-card bg-surface p-6 text-center shadow-card sm:p-8">
        <BrandMark className="mx-auto mb-6" />
        {children}
      </section>
    </main>
  );
}
