/**
 * Every Settings panel, from app.js `show*Panel()`. Each `open…` function
 * checks what it needs, then opens the one panel with a component that loads
 * its own data.
 */
import { useEffect, useRef, useState } from "react";
import { Bot, Check, Globe, Hexagon, Laptop, Monitor, Puzzle, ScrollText, Smartphone, Tablet, Usb, type LucideIcon } from "lucide-react";
import { fingerprintOrKey, isSessionValid } from "@poweur/client";
import { loadPolicy, loadProfile, savePolicy } from "../../actions/account";
import { activeClient, errorMessage, resolveForActive } from "../../actions/relay";
import { refreshRelaySession, revokeRelaySession } from "../../actions/settings";
import { askConfirm } from "../../components/Dialogs";
import { PolicyControls } from "../../components/PolicyControls";
import { ProfileEditor } from "../../components/ProfileEditor";
import { cn } from "../../lib/cn";
import { identityApiFor, lookup } from "../../lib/client.js";
import { describeDeviceRow, mergeKeysAndDevices, restoreNote } from "../../lib/devices.js";
import {
  buildRecoveryKit,
  enrollThisBrowser,
  listEnrollments,
  removeEnrollment,
  verifyRecoveryKit,
} from "../../lib/keystore.js";
import { readConnectedApps, readConsentLog, revokeConnectedApp } from "../../lib/signin.js";
import { getConfig, getUnlockedKeys, isShellRuntime, loadIdentityRecord, loadSessionRecord, relayUrlFor, saveConfig } from "../../lib/storage.js";
import { useData } from "../../state/data";
import { useRoute } from "../../state/route";
import { setAnalyticsConsent } from "../../lib/observability";
import { refreshSession, useSession } from "../../state/session";
import { closePanel, openPanel, setLoading, toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Chip, KvRow, Notice, SectionLabel } from "../../ui/Display";
import { InfoTip } from "../../ui/InfoTip";
import { FormGroup, Input, inputClass, Label, Textarea } from "../../ui/Field";

const activeIdentity = () => useSession.getState().identity ?? "";

function requireClient(): any {
  const client = activeClient();
  if (!client) toast("Unlock your identity first", "warning");
  return client;
}

const fmtTime = (timestamp: string) => {
  try {
    return new Date(timestamp).toLocaleString();
  } catch {
    return timestamp;
  }
};

// ─── Inbox policy ─────────────────────────────────────────────────────────────

export function openPolicyPanel() {
  if (!requireClient()) return;
  openPanel("Inbox", (close) => <PolicyPanel close={close} />);
}

function PolicyPanel({ close }: { close: () => void }) {
  const policy = useData((state) => state.policy);
  const [requested, setRequested] = useState(false);
  useEffect(() => {
    void loadPolicy({ force: true }).finally(() => setRequested(true));
  }, []);
  if (!requested || policy.loading) return <p className="text-muted">Loading…</p>;
  return (
    <PolicyControls
      policy={policy.doc ?? {}}
      explicit={policy.explicit}
      onSave={async (document) => {
        // One write of the whole document: mode and the anonymous block live together.
        await savePolicy(document);
        useData.setState((state) => ({ anon: { ...state.anon, loaded: false } }));
        toast("Inbox policy saved", "success");
        close();
      }}
    />
  );
}

// ─── Profile ──────────────────────────────────────────────────────────────────

export function openProfilePanel() {
  if (!requireClient()) return;
  openPanel("Your profile", (close) => <ProfilePanel close={close} />);
}

function ProfilePanel({ close }: { close: () => void }) {
  const profile = useData((state) => state.profile);
  const [requested, setRequested] = useState(false);
  const [features, setFeatures] = useState<string[]>([]);
  useEffect(() => {
    void loadProfile({ force: true }).finally(() => setRequested(true));
    // Capabilities are read-only: what this identity speaks, not a preference.
    resolveForActive(activeIdentity()).then(
      (entry: any) => setFeatures(Object.keys(entry?.capabilities?.features ?? {})),
      () => {},
    );
  }, []);
  if (!requested || profile.loading) return <p className="text-muted">Loading…</p>;
  return (
    <>
      <ProfileEditor
        profile={profile.doc}
        onSaved={() => {
          toast("Profile saved", "success");
          close();
        }}
      />
      {features.length > 0 && (
        <div className="profile-caps mt-4">
          <SectionLabel className="px-0">This identity speaks</SectionLabel>
          <div className="profile-card-caps flex flex-wrap gap-1.5">
            {features.map((feature) => (
              <Chip key={feature}>{feature}</Chip>
            ))}
          </div>
        </div>
      )}
    </>
  );
}

// ─── Connected apps (EPIC-008) ────────────────────────────────────────────────

export function openConnectedAppsPanel() {
  if (!requireClient()) return;
  openPanel("Connected apps", () => <ConnectedApps />);
}

function ConnectedApps() {
  const [state, setState] = useState<{ apps: any[]; recent: any[]; dav: any } | { error: string } | null>(null);
  useEffect(() => {
    const identity = activeIdentity();
    let live = true;
    void (async () => {
      try {
        const client = requireClient();
        const dav = client?.system();
        const [doc, log] = await Promise.all([readConnectedApps(dav), client ? client.consentLog().then((entries: any) => readConsentLog(entries)) : []]);
        if (!live || activeIdentity() !== identity) return;
        const apps = [...(doc.apps ?? [])].sort((a: any, b: any) => String(a.app_id).localeCompare(String(b.app_id)));
        setState({ apps, recent: log.slice(-10).reverse(), dav });
      } catch (error) {
        if (live) setState({ error: `Could not load connected apps: ${errorMessage(error)}` });
      }
    })();
    return () => {
      live = false;
    };
  }, []);

  if (!state) return <p role="status">Loading…</p>;
  if ("error" in state) return <p role="status">{state.error}</p>;

  const revoke = async (appId: string) => {
    const confirmed = await askConfirm({
      title: "Revoke app",
      message: `Revoke ${appId}? Its current tokens stop working immediately.`,
      confirmLabel: "Revoke",
    });
    if (!confirmed) {
      openConnectedAppsPanel();
      return;
    }
    try {
      await revokeConnectedApp(state.dav, appId);
      toast("App access revoked", "success");
    } catch (error) {
      toast(errorMessage(error), "error");
    }
    openConnectedAppsPanel();
  };

  return (
    <div id="connected-apps-host">
      {state.apps.length ? (
        <div className="settings-rows overflow-hidden rounded-card bg-surface-2">
          {state.apps.map((app) => (
            <div key={app.app_id} className="settings-row flex items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0">
              <span className="settings-row-icon flex w-7 shrink-0 justify-center text-accent">
                <Puzzle className="size-5" strokeWidth={1.9} aria-hidden="true" />
              </span>
              <span className="settings-row-label flex-1">
                <strong>{app.name || app.app_id}</strong>
                <br />
                <span className="text-[13px] text-muted">
                  {app.audience}
                  <br />
                  {(app.scopes || []).join(", ")}
                </span>
              </span>
              {app.revoked_at ? (
                <span className="settings-row-value text-muted">Revoked</span>
              ) : (
                <Button size="sm" variant="secondary" data-revoke-app={app.app_id} onClick={() => void revoke(app.app_id)}>
                  Revoke
                </Button>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-muted">No apps have access to your home.</p>
      )}
      <SectionLabel className="mt-4 px-0">Recent approvals</SectionLabel>
      {state.recent.length ? (
        <div className="settings-rows overflow-hidden rounded-card bg-surface-2">
          {state.recent.map((record, index) => (
            <div key={`${record.at}-${index}`} className="settings-row flex items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0">
              <span className="settings-row-icon flex w-7 shrink-0 justify-center text-success">
                <Check className="size-5" aria-hidden="true" />
              </span>
              <span className="settings-row-label flex-1">
                <strong>{record.app_name || record.app_id || record.audience}</strong>
                <br />
                <span className="text-[13px] text-muted">
                  {record.at || ""} · {(record.scopes || []).join(", ") || "sign-in only"}
                </span>
              </span>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-[13px] text-muted">No approvals recorded yet.</p>
      )}
    </div>
  );
}

// ─── Keys & devices (EPIC-011) ────────────────────────────────────────────────

const ENROLLMENT_KIND_LABEL: Record<string, string> = {
  passkey: "Passkey",
  "hardware-key": "Hardware key",
  "cli-passphrase": "CLI passphrase",
  "recovery-kit": "Recovery kit",
  native: "Native keystore",
};

const ENROLLMENT_ICON: Record<string, LucideIcon> = { "hardware-key": Usb, "recovery-kit": ScrollText };

const DEVICE_ICON: Record<string, LucideIcon> = {
  laptop: Laptop,
  desktop: Monitor,
  phone: Smartphone,
  tablet: Tablet,
  browser: Globe,
  agent: Bot,
};

function RowIcon({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span className="enrollment-icon flex w-8 shrink-0 justify-center text-accent">
      <Icon className="size-[22px]" strokeWidth={1.8} aria-hidden="true" />
    </span>
  );
}

const ENROLLMENT_WRAP_LABEL: Record<string, string> = {
  prf: "passkey (PRF)",
  passphrase: "passphrase",
  native: "the OS keystore",
};

export async function openKeysAndDevicesPanel() {
  const identity = activeIdentity();
  const client = requireClient();
  if (!client) return;

  setLoading(true, "Reading your devices…");
  let enrollments: any[];
  try {
    enrollments = await listEnrollments(client, identity);
  } catch (error) {
    setLoading(false);
    toast(`Could not read your devices: ${errorMessage(error)}`, "error", 7000);
    return;
  }
  // The device registry is a different list: enrollments are who can unlock,
  // the registry is who is using it. Losing it costs the section, not the panel.
  let registry: any[] | null;
  try {
    registry = (await client.devices().list()).devices ?? [];
  } catch {
    registry = null;
  }
  setLoading(false);
  openPanel("Keys & devices", () => <KeysAndDevices identity={identity} enrollments={enrollments} registry={registry} />);
}

type MergedRow = { id: string; device?: any; enrollment?: any; current: boolean };

function KeysAndDevices({ identity, enrollments, registry }: { identity: string; enrollments: any[]; registry: any[] | null }) {
  const thisEnrolled = enrollments.some((enrollment) => enrollment.current);
  const rows = mergeKeysAndDevices(enrollments, registry ?? []);
  const here = isShellRuntime() ? "device" : "browser";
  const loseHow = isShellRuntime() ? "clearing app data" : "clearing site data";

  const backUp = async () => {
    closePanel();
    setLoading(true, `Backing up this ${here}…`);
    try {
      await enrollThisBrowser(activeClient(), identity);
      setLoading(false);
      toast(`This ${here} is backed up`, "success", 3500);
    } catch (error) {
      setLoading(false);
      toast(errorMessage(error), "error", 8000);
    }
    void openKeysAndDevicesPanel();
  };

  // One action per row: drop its keystore copy if it has one, and end the
  // sessions of the device the relay has seen for it.
  const removeRow = async (row: MergedRow) => {
    const { device, enrollment } = row;
    const confirmed = await askConfirm({
      title: "Remove device",
      message: "Sign this device out and delete its backup? If it may be compromised, rotate your keys afterwards — a device that has your key can sign back in.",
      confirmLabel: "Remove",
    });
    if (confirmed) {
      setLoading(true, "Removing device…");
      try {
        if (enrollment) await removeEnrollment(activeClient(), identity, enrollment.enrollment_id);
        if (device) {
          const result = await activeClient().devices().revoke(device.id);
          toast(`Removed: ${result.sessions_revoked} session(s) ended`, "success", 5000);
        } else {
          toast("Device removed", "success");
        }
        setLoading(false);
      } catch (error) {
        setLoading(false);
        toast(errorMessage(error), "error", 8000);
      }
    }
    void openKeysAndDevicesPanel();
  };

  return (
    <div>
      {!thisEnrolled && (
        <Notice tone="warn">
          <strong>This {here} is not backed up.</strong> Its copy of your keys exists only here, so {loseHow} would destroy this identity.
          Registering it stores an encrypted copy the relay cannot read.
          <Button id="btn-enroll-this" size="sm" className="mt-2.5" onClick={() => void backUp()}>
            Back up this {here}
          </Button>
        </Notice>
      )}

      {rows.length ? (
        <div className="enrollment-list flex flex-col">
          {rows.map((row) => {
            const { device, enrollment } = row;
            const title = device?.name || enrollment?.label || (enrollment && (ENROLLMENT_KIND_LABEL[enrollment.kind] ?? enrollment.kind)) || "Unnamed device";
            const revoked = Boolean(device?.revoked);
            const kind = device?.kind ?? "";
            return (
              <div key={row.id} className={cn("enrollment-row flex min-h-15 items-center gap-3 border-b border-sep py-3 last:border-b-0", revoked && "is-revoked opacity-55")}>
                <RowIcon icon={(enrollment && ENROLLMENT_ICON[enrollment.kind]) || DEVICE_ICON[kind] || (enrollment ? Smartphone : Hexagon)} />
                <div className="enrollment-body min-w-0 flex-1">
                  <div className="enrollment-label flex flex-wrap items-center gap-1.5 text-[15px] font-semibold">
                    {title}
                    {row.current && <Chip tone="success">this device</Chip>}
                    {enrollment?.role === "recovery-master" && <Chip tone="warning">recovery master</Chip>}
                    {revoked && <Chip tone="warning">revoked</Chip>}
                  </div>
                  <div className="enrollment-meta mt-0.5 text-[13px] text-muted">
                    {device ? describeDeviceRow(device) : `${ENROLLMENT_KIND_LABEL[enrollment.kind] ?? enrollment.kind}${enrollment.created_at ? ` · added ${fmtTime(enrollment.created_at)}` : ""}`}
                  </div>
                  <div className="enrollment-restore mt-0.5">
                    <InfoTip label={enrollment?.has_passkey && enrollment.wrap === "prf" ? "Restorable" : "Not restorable"}>
                      {restoreNote(row)}
                      {enrollment ? ` Unlocked by ${ENROLLMENT_WRAP_LABEL[enrollment.wrap] ?? enrollment.wrap}.` : ""}
                    </InfoTip>
                  </div>
                </div>
                {!revoked && (
                  <Button
                    size="sm"
                    variant="secondary"
                    data-remove-enrollment={enrollment?.enrollment_id}
                    data-revoke-device={device?.id}
                    disabled={row.current}
                    aria-label={`Remove ${title}`}
                    onClick={() => void removeRow(row)}
                  >
                    Remove
                  </Button>
                )}
              </div>
            );
          })}
        </div>
      ) : (
        <p className="text-[13px] text-muted">No devices registered yet.</p>
      )}
      {registry === null && <p className="mt-2 text-[13px] text-muted">Could not read when devices were last used.</p>}

      <Button
        id="btn-enroll-device"
        className="mt-4"
        onClick={() => {
          closePanel();
          useRoute.getState().push("pair");
        }}
      >
        Add a device
      </Button>

      <div className="mt-2.5">
        <InfoTip label="What removing means">
          Removing signs the device out and deletes its backup. It does not change your key, so a device that already has it could sign
          back in. If you think one is compromised, rotate your keys.
        </InfoTip>
      </div>
    </div>
  );
}

// ─── Recovery kit (EPIC-011) ──────────────────────────────────────────────────

export function openRecoveryKitPanel() {
  const identity = activeIdentity();
  openPanel("Recovery kit", () => <RecoveryKit identity={identity} />);
}

function RecoveryKit({ identity }: { identity: string }) {
  const keys: any = getUnlockedKeys();
  const [checking, setChecking] = useState(false);
  const [result, setResult] = useState<boolean | null>(null);
  const typed = useRef<HTMLTextAreaElement>(null);

  if (!keys?.seed) return <Notice tone="warn">Unlock this identity to see its recovery kit.</Notice>;

  const words = (buildRecoveryKit(identity, keys.seed) as { mnemonic: string }).mnemonic.split(" ");
  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        Write these 24 words down and keep them somewhere safe and offline. Anyone who has them <strong>is</strong> you — and without them,
        losing every device loses this identity.
      </p>
      <ol className="mnemonic my-1 ml-5 grid list-decimal grid-cols-2 gap-x-3.5 gap-y-1.5 font-mono text-[15px] min-[420px]:grid-cols-3">
        {words.map((word, index) => (
          <li key={index} className="mnemonic-word py-[3px]">
            {word}
          </li>
        ))}
      </ol>
      <Button id="btn-verify-kit" className="mt-4" onClick={() => setChecking(true)}>
        I've written it down — check it
      </Button>
      {checking && (
        <div id="kit-verify">
          <FormGroup className="mt-4">
            <Label htmlFor="kit-input">Type the 24 words back</Label>
            <Textarea ref={typed} id="kit-input" rows={4} className="min-h-0" placeholder="word1 word2 …" spellCheck={false} />
          </FormGroup>
          <Button id="btn-check-kit" onClick={() => setResult(Boolean(verifyRecoveryKit(typed.current?.value ?? "", keys.seed)))}>
            Check
          </Button>
          <p id="kit-result" className={cn("mt-2.5 min-h-5 text-[13px]", result === true && "val-ok text-success", result === false && "val-warn text-warning")}>
            {result === true && "✓ That's your kit. Store it somewhere safe."}
            {result === false && "✕ That doesn't match. Check the spelling and the order."}
          </p>
        </div>
      )}
    </div>
  );
}

// ─── Identity keys, lookup, relay, session, DNS ───────────────────────────────

export function openIdentityKeysPanel() {
  const record: any = loadIdentityRecord(activeIdentity());
  if (!record) return;
  openPanel("Identity keys", () => (
    <div>
      <KvRow label="Identity" mono>
        {record.identity}
      </KvRow>
      <KvRow label="Signing key" mono>
        {String(record.publicKey ?? "").slice(0, 24)}…
      </KvRow>
      <KvRow label="Enc key" mono>
        {String(record.encPublicKey ?? "").slice(0, 24)}…
      </KvRow>
      <KvRow label="Relay">{record.relay}</KvRow>
      <KvRow label="Created">{fmtTime(record.createdAt)}</KvRow>
      <KvRow label="Protection">
        <Chip tone="success">{record.encryptedKeys?.kdf === "native" ? "Device keystore" : "Passkey PRF"}</Chip>
      </KvRow>
      <KvRow label="DNS" mono>
        _poweur.{record.identity}
        <br />
        _poweur-enc.{record.identity}
      </KvRow>
    </div>
  ));
}

export function openLookupPanel() {
  openPanel("Lookup identity", () => <LookupPanel />);
}

function LookupPanel() {
  const field = useRef<HTMLInputElement>(null);
  const [output, setOutput] = useState("");
  const run = async () => {
    const id = field.current?.value.trim().toLowerCase();
    if (!id) {
      toast("Enter an identity", "warning");
      return;
    }
    setOutput("Looking up…");
    try {
      const { source, document } = (await lookup(id, relayUrlFor(activeIdentity()))) as any;
      setOutput(
        [
          `identity: ${document.identity}`,
          `source: ${source}`,
          `public_key: ${document.public_key || ""}`,
          `safety number: ${document.public_key ? fingerprintOrKey(document.public_key) : ""}`,
          `encryption_public_key: ${document.encryption_public_key || ""}`,
          `relay: ${document.relay || ""}`,
          `capabilities: ${(document.capabilities || []).join(", ")}`,
        ].join("\n"),
      );
    } catch (error) {
      setOutput(`Error: ${errorMessage(error)}`);
      toast(errorMessage(error), "error");
    }
  };
  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        Resolve keys via well-known / relay API (then DNS), like <code>poweur identity lookup</code>.
      </p>
      <FormGroup>
        <Label htmlFor="lookup-id">Identity</Label>
        <Input ref={field} id="lookup-id" type="text" placeholder="bob.poweur.net" autoComplete="off" spellCheck={false} />
      </FormGroup>
      <Button id="btn-lookup-run" onClick={() => void run()}>
        Lookup
      </Button>
      <pre id="lookup-result" className="mt-3.5 font-mono text-[13px] break-all whitespace-pre-wrap">
        {output}
      </pre>
    </div>
  );
}

export function openRelayPanel() {
  openPanel("Relay", (close) => <RelayPanel close={close} />);
}

function RelayPanel({ close }: { close: () => void }) {
  const field = useRef<HTMLInputElement>(null);
  const [status, setStatus] = useState<{ text: string; tone: "" | "ok" | "err" }>({ text: "", tone: "" });
  return (
    <div>
      <FormGroup>
        <Label htmlFor="panel-relay-url">Relay URL</Label>
        <Input ref={field} id="panel-relay-url" type="url" defaultValue={getConfig().relayUrl ?? ""} />
      </FormGroup>
      <div className="mt-1 flex gap-2.5">
        <Button
          id="panel-test-relay"
          size="sm"
          variant="secondary"
          className="flex-1"
          onClick={async () => {
            setStatus({ text: "Testing…", tone: "" });
            try {
              await (identityApiFor(field.current?.value.trim() ?? "") as any).health();
              setStatus({ text: "✓ Connected", tone: "ok" });
            } catch (error) {
              setStatus({ text: `✕ ${errorMessage(error)}`, tone: "err" });
            }
          }}
        >
          Test
        </Button>
        <Button
          id="panel-save-relay"
          size="sm"
          className="flex-1"
          onClick={() => {
            saveConfig({ ...getConfig(), relayUrl: field.current?.value.trim() ?? "" });
            refreshSession();
            toast("Saved", "success");
            close();
          }}
        >
          Save
        </Button>
      </div>
      <p id="panel-relay-status" className={cn("mt-2.5 min-h-[18px] text-[13px] text-muted", status.tone === "ok" && "text-success", status.tone === "err" && "text-danger")}>
        {status.text}
      </p>
    </div>
  );
}

export function openSessionPanel() {
  const session: any = loadSessionRecord(activeIdentity());
  if (!session) return;
  const valid = isSessionValid(session);
  openPanel("Session", (close) => (
    <div>
      <KvRow label="Status">
        <Chip tone={valid ? "success" : "danger"}>{valid ? "Active" : "Expired"}</Chip>
      </KvRow>
      <KvRow label="Session ID" mono>
        {session.sessionId}
      </KvRow>
      <KvRow label="Issued">{fmtTime(session.issuedAt)}</KvRow>
      <KvRow label="Expires">{fmtTime(session.expiresAt)}</KvRow>
      <div className="stack mt-4 flex flex-col gap-3">
        <Button
          id="panel-refresh-sess"
          onClick={() => {
            close();
            void refreshRelaySession();
          }}
        >
          Refresh session
        </Button>
        <Button
          id="panel-revoke-sess"
          variant="ghost"
          className="text-danger"
          onClick={() => {
            close();
            void revokeRelaySession();
          }}
        >
          Revoke session
        </Button>
      </div>
    </div>
  ));
}

export function openDnsPanel() {
  openPanel("DNS provider", (close) => <DnsPanel close={close} />);
}

function DnsPanel({ close }: { close: () => void }) {
  const provider = useRef<HTMLSelectElement>(null);
  const parent = useRef<HTMLInputElement>(null);
  const config = getConfig();
  return (
    <div>
      <FormGroup>
        <Label htmlFor="panel-dns-provider">Provider</Label>
        <select ref={provider} id="panel-dns-provider" className={cn(inputClass, "cursor-pointer")} defaultValue={config.dnsProvider || "cloudflare"}>
          <option value="cloudflare">Cloudflare</option>
          <option value="hetzner">Hetzner</option>
        </select>
      </FormGroup>
      <FormGroup>
        <Label htmlFor="panel-parent-domain">Parent domain</Label>
        <Input ref={parent} id="panel-parent-domain" type="text" defaultValue={config.parentDomain ?? ""} placeholder="poweur.net" />
      </FormGroup>
      <Button
        id="panel-save-dns"
        onClick={() => {
          saveConfig({ ...getConfig(), dnsProvider: provider.current?.value, parentDomain: parent.current?.value.trim() });
          refreshSession();
          toast("Saved", "success");
          close();
        }}
      >
        Save
      </Button>
    </div>
  );
}

// ─── Diagnostics: include my ID (EPIC-013 consent) ───────────────────────────

export function openAnalyticsPanel() {
  if (!requireClient()) return;
  openPanel("Diagnostics", (close) => <AnalyticsPanel close={close} />);
}

function AnalyticsPanel({ close }: { close: () => void }) {
  const identity = activeIdentity();
  const [preference, setPreference] = useState<{ granted: boolean } | { error: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const box = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let live = true;
    requireClient()
      ?.analyticsPreference()
      .then(
        (pref: any) => live && activeIdentity() === identity && setPreference({ granted: Boolean(pref?.granted) }),
        (error: unknown) => live && setPreference({ error: `Could not load preference: ${errorMessage(error)}` }),
      );
    return () => {
      live = false;
    };
  }, []);

  return (
    <div>
      <p className="mb-3">
        To keep Poweur working, the app and relay always record anonymous diagnostics: which screens are used, errors and speed, with no
        Poweur ID, no IP address and nothing stored on your device. Including your ID lets us find and fix problems you run into: the relay
        then records your ID and IP with your requests, and the app's error reports carry your ID. Message contents, files and keys are never
        included.
      </p>
      <div id="analytics-host" role="status">
        {!preference ? (
          "Loading…"
        ) : "error" in preference ? (
          preference.error
        ) : (
          <>
            <label className="flex items-center gap-2">
              <input ref={box} id="analytics-consent" type="checkbox" className="size-5 accent-accent" defaultChecked={preference.granted} />
              Include my ID ({identity}) in diagnostics
            </label>
            <p className="my-2 text-[13px] text-muted">Changes affect future exports. Existing records expire under the relay's retention settings.</p>
            <Button
              id="analytics-save"
              disabled={saving}
              onClick={async () => {
                if (activeIdentity() !== identity) return;
                setSaving(true);
                try {
                  const granted = Boolean(box.current?.checked);
                  await requireClient()?.setAnalyticsConsent(granted);
                  setAnalyticsConsent(identity, granted);
                  toast("Analytics preference saved", "success");
                  close();
                } catch (error) {
                  toast(errorMessage(error), "error");
                  setSaving(false);
                }
              }}
            >
              Save
            </Button>
          </>
        )}
      </div>
    </div>
  );
}
