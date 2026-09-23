import { useEffect, useRef, useState } from "react";
import type { Launcher } from "../lib/page";

type Verdict = { available?: boolean; identity?: string; reason?: string; message?: string };

const TYPE_DELAY_MS = 450;
// The relay allows about five availability checks a minute per address; the
// watch leaves room for typing, and focus (coming back to this tab) is the
// moment that matters anyway.
const WATCH_EVERY_MS = 20_000;
const WATCH_FOR_MS = 30 * 60_000;

export function useCreateID(launcher: Launcher, txn: string, onReady: (id: string) => void) {
  const suffix = `.${launcher.domain}`;
  const [input, setInput] = useState("");
  const [status, setStatus] = useState("");
  const [tone, setTone] = useState<"" | "ok" | "bad">("");
  const [available, setAvailable] = useState(false);
  const handle = normalize(input, suffix);
  const backoffUntil = useRef(0);
  const watching = useRef({ name: "", until: 0 });
  const readyRef = useRef(onReady);
  readyRef.current = onReady;

  async function ask(name: string): Promise<Verdict | null> {
    if (Date.now() < backoffUntil.current) return null;
    const url = `${launcher.url}/hosted/availability?handle=${encodeURIComponent(name)}&domain=${encodeURIComponent(launcher.domain)}`;
    const res = await fetch(url, { credentials: "omit", cache: "no-store" });
    if (res.status === 429) {
      backoffUntil.current = Date.now() + 60_000;
      return null;
    }
    return res.ok ? ((await res.json()) as Verdict) : null;
  }

  // Check the name as it is typed.
  useEffect(() => {
    setAvailable(false);
    if (!handle) {
      setStatus("");
      setTone("");
      return;
    }
    setStatus("Checking…");
    setTone("");
    let current = true;
    const timer = setTimeout(async () => {
      try {
        const verdict = await ask(handle);
        if (!current) return;
        if (!verdict) {
          setStatus("Couldn't check right now — you can still create it.");
          return;
        }
        setAvailable(Boolean(verdict.available));
        setTone(verdict.available ? "ok" : "bad");
        setStatus(verdict.available ? `${verdict.identity ?? handle + suffix} is available` : verdict.message || "Not available");
      } catch {
        if (current) setStatus("Couldn't check right now — you can still create it.");
      }
    }, TYPE_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [handle]);

  // Watch for the name to be taken, from the moment the launcher opened.
  useEffect(() => {
    const look = async () => {
      const w = watching.current;
      if (!w.name || Date.now() > w.until) return;
      try {
        const verdict = await ask(w.name);
        if (verdict && !verdict.available && verdict.reason === "taken") {
          watching.current = { name: "", until: 0 };
          readyRef.current(verdict.identity || w.name + suffix);
        }
      } catch {
        /* try again later */
      }
    };
    const timer = setInterval(look, WATCH_EVERY_MS);
    const onVisible = () => document.visibilityState === "visible" && look();
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("focus", look);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("focus", look);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const q = new URLSearchParams({ from: "signin" });
  if (handle && available) q.set("handle", handle);
  const href = `${launcher.url}/app/?${q}`;

  const onOpen = () => {
    // Creating an ID takes longer than a sign-in normally waits.
    fetch(`/t/${txn}/creating`, { method: "POST", credentials: "same-origin" }).catch(() => {});
    if (handle && available) {
      watching.current = { name: handle, until: Date.now() + WATCH_FOR_MS };
      setStatus(`Create ${handle}${suffix} in the new tab, then come back.`);
      setTone("");
    } else {
      setStatus("Create your ID in the new tab, then come back and enter it above.");
      setTone("");
    }
  };

  return { input, setInput, status, tone, available, handle, href, onOpen };
}

export function normalize(value: string, suffix: string): string {
  let v = value.trim().toLowerCase().replace(/^@/, "");
  if (v.endsWith(suffix)) v = v.slice(0, -suffix.length);
  return v;
}
