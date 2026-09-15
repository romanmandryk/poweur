/**
 * Which relay, when nothing else can answer (EPIC-019 E19-T1). A relay-served
 * page is *on* its relay; a shell runs from its own bundle and has to name one.
 * The picker stays in the shell after a default is known, because switching
 * relays is why the shell exists.
 */
import { useEffect, useRef, useState } from "react";
import { cn } from "../../lib/cn";
import { relayUrlForPreset } from "../../lib/claim";
import { resolveMode } from "../../lib/mode.js";
import {
  defaultRelayUrl,
  getConfig,
  hasRelayUrl,
  isShellRuntime,
  localDevRelayUrl,
  PRODUCTION_RELAY_URL,
  relayPresetFor,
  saveConfig,
} from "../../lib/storage.js";
import { refreshSession, useSession, type ModeInfo } from "../../state/session";
import { inputClass, Label } from "../../ui/Field";

export function relayPromptVisible(): boolean {
  return isShellRuntime() || !hasRelayUrl();
}

const presets = () => ({ production: PRODUCTION_RELAY_URL as string, local: localDevRelayUrl() as string });

export function RelayPrompt({ className }: { className?: string }) {
  useSession((state) => state.config); // repaint when the saved relay changes
  const [initial] = useState(() => {
    const current: string = defaultRelayUrl();
    const preset: string = relayPresetFor(current);
    return { current, preset };
  });
  const [preset, setPreset] = useState(initial.preset);
  const [status, setStatus] = useState<{ text: string; tone: "" | "ok" | "warn" }>({ text: "", tone: "" });
  const typed = useRef<HTMLInputElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const apply = async (chosen: string, { probe }: { probe: boolean }) => {
    const url = relayUrlForPreset(chosen, typed.current?.value, presets());
    if (!url) {
      setStatus({ text: "", tone: "" });
      return;
    }
    if (!probe) {
      setStatus({ text: `Using ${new URL(url).host}`, tone: "ok" });
      return;
    }
    setStatus({ text: "Checking…", tone: "" });
    try {
      // Checked rather than taken on faith: a typo would otherwise surface as
      // an unexplained failure three screens later.
      const health = await fetch(new URL("/health", url).toString(), { headers: { Accept: "application/json" } }).then((r) =>
        r.ok ? r.json() : null,
      );
      if (!health?.status) throw new Error("that does not look like a relay");
      saveConfig({ ...getConfig(), relayUrl: url });
      refreshSession();
      setStatus({ text: `Connected to ${new URL(url).host}`, tone: "ok" });
      // The relay it was just given is the only thing that can say what it
      // may claim under (E15-T7).
      void resolveMode({ force: true }).then((info: ModeInfo) => useSession.setState({ mode: info }));
    } catch (error) {
      setStatus({ text: `Could not reach ${url} — ${(error as Error).message}`, tone: "warn" });
    }
  };

  useEffect(() => {
    // First paint of the default: no probe, or the landing loops on its own seed.
    if (preset !== "custom" && initial.current === relayUrlForPreset(preset, "", presets())) void apply(preset, { probe: false });
    else if (preset === "custom" && initial.current) void apply(preset, { probe: false });
    return () => clearTimeout(timer.current);
  }, []);

  return (
    <div className={cn("form-card relay-prompt mb-4 rounded-card bg-surface p-5", className)}>
      <Label htmlFor="setup-relay-preset">Relay</Label>
      <p className="mb-1.5 text-[13px] text-muted">
        Where new identities are created. poweur.net is the hosted relay; switch to a local or your own for testing.
      </p>
      <select
        id="setup-relay-preset"
        className={cn(inputClass, "cursor-pointer")}
        value={preset}
        onChange={(event) => {
          const next = event.currentTarget.value;
          setPreset(next);
          clearTimeout(timer.current);
          if (next === "custom") {
            setStatus({ text: "", tone: "" });
            setTimeout(() => typed.current?.focus(), 0);
            return;
          }
          void apply(next, { probe: true });
        }}
      >
        <option value="production">poweur.net</option>
        <option value="local">Local emulator ({String(presets().local).replace(/^https?:\/\//, "")})</option>
        <option value="custom">Other…</option>
      </select>
      <input
        ref={typed}
        id="setup-relay"
        className={cn(inputClass, "mt-2")}
        type="url"
        inputMode="url"
        autoCapitalize="none"
        autoCorrect="off"
        autoComplete="off"
        spellCheck={false}
        placeholder="https://relay.example.com"
        defaultValue={initial.preset === "custom" ? initial.current : ""}
        hidden={preset !== "custom"}
        onInput={() => {
          clearTimeout(timer.current);
          timer.current = setTimeout(() => void apply("custom", { probe: true }), 600);
        }}
        onKeyDown={(event) => {
          if (event.key !== "Enter") return;
          clearTimeout(timer.current);
          void apply("custom", { probe: true });
        }}
      />
      <p
        id="setup-relay-status"
        role="status"
        aria-live="polite"
        className={cn(
          "idin-status mt-1.5 min-h-[18px] text-[13px] text-muted",
          status.tone === "ok" && "val-ok text-success",
          status.tone === "warn" && "val-warn text-warning",
        )}
      >
        {status.text}
      </p>
    </div>
  );
}
