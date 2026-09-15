/**
 * Every Settings panel, from app.js `show*Panel()`. Each `open…` function
 * checks what it needs, then opens the one panel with a component that loads
 * its own data.
 */
import { useEffect, useRef, useState } from "react";
import { fingerprintOrKey, isSessionValid, normalizeRendezvousId } from "@poweur/client";
import { loadPolicy, loadProfile, savePolicy } from "../../actions/account";
import { enrollApiForJoin } from "../../actions/identity";
import { activeClient, errorMessage, resolveForActive } from "../../actions/relay";
import { refreshRelaySession, revokeRelaySession } from "../../actions/settings";
import { askConfirm } from "../../components/Dialogs";
import { PolicyControls } from "../../components/PolicyControls";
import { ProfileEditor } from "../../components/ProfileEditor";
import { cn } from "../../lib/cn";
import { identityApiFor, lookup } from "../../lib/client.js";
import { describeDevice, deviceIcon } from "../../lib/devices.js";
import {
  buildRecoveryKit,
  enrollThisBrowser,
  listEnrollments,
  recoveryKitEligibility,
  removeEnrollment,
  verifyRecoveryKit,
} from "../../lib/keystore.js";
import { readConnectedApps, readConsentLog, revokeConnectedApp } from "../../lib/signin.js";
import { getConfig, getUnlockedKeys, isShellRuntime, loadIdentityRecord, loadSessionRecord, relayUrlFor, saveConfig } from "../../lib/storage.js";
import { fromBase64url } from "../../lib/vault.js";
import { useData } from "../../state/data";
import { refreshSession, useSession } from "../../state/session";
import { closePanel, openPanel, setLoading, toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Chip, KvRow, Notice, SectionLabel } from "../../ui/Display";
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
        const dav = await requireClient()?.dav();
        const [doc, log] = await Promise.all([readConnectedApps(dav), readConsentLog(dav)]);
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
              <span className="settings-row-icon w-7 text-center text-xl">🧩</span>
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
              <span className="settings-row-icon w-7 text-center">✓</span>
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
    registry = (await (await client.dav()).devices()).devices ?? [];
  } catch {
    registry = null;
  }
  setLoading(false);
  openPanel("Keys & devices", () => <KeysAndDevices identity={identity} enrollments={enrollments} registry={registry} />);
}

function KeysAndDevices({ identity, enrollments, registry }: { identity: string; enrollments: any[]; registry: any[] | null }) {
  const record: any = loadIdentityRecord(identity);
  const thisEnrolled = enrollments.some((enrollment) => enrollment.current);
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

  const removeDevice = async (enrollmentId: string) => {
    const confirmed = await askConfirm({
      title: "Remove device",
      message: "Remove this device? It will lose access to your stored keys and its sessions end.",
      confirmLabel: "Remove",
    });
    if (confirmed) {
      setLoading(true, "Removing device…");
      try {
        await removeEnrollment(activeClient(), identity, enrollmentId);
        setLoading(false);
        toast("Device removed", "success");
      } catch (error) {
        setLoading(false);
        toast(errorMessage(error), "error", 8000);
      }
    }
    void openKeysAndDevicesPanel();
  };

  const revokeDevice = async (deviceId: string) => {
    const confirmed = await askConfirm({
      title: "Revoke device",
      message: "Revoke this device? Its sessions, tokens and app passwords stop working.",
      confirmLabel: "Revoke",
    });
    if (confirmed) {
      setLoading(true, "Revoking device…");
      try {
        const result = await (await activeClient().dav()).revokeDevice(deviceId);
        setLoading(false);
        toast(
          `Revoked: ${result.sessions_revoked} session(s), ${result.dav_tokens_revoked} token(s), ${result.app_passwords_revoked} app password(s)`,
          "success",
          5000,
        );
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

      {enrollments.length ? (
        <div className="enrollment-list flex flex-col">
          {enrollments.map((enrollment) => (
            <div key={enrollment.enrollment_id} className="enrollment-row flex min-h-15 items-center gap-3 border-b border-sep py-3 last:border-b-0">
              <span className="enrollment-icon w-8 text-center text-[22px]">
                {enrollment.kind === "hardware-key" ? "🔐" : enrollment.kind === "recovery-kit" ? "🧾" : "📱"}
              </span>
              <div className="enrollment-body min-w-0 flex-1">
                <div className="enrollment-label flex flex-wrap items-center gap-1.5 text-[15px] font-semibold">
                  {enrollment.label || ENROLLMENT_KIND_LABEL[enrollment.kind] || enrollment.kind}
                  {enrollment.current && <Chip tone="success">this device</Chip>}
                  {enrollment.role === "recovery-master" && <Chip tone="warning">recovery master</Chip>}
                </div>
                <div className="enrollment-meta mt-0.5 text-[13px] text-muted">
                  {ENROLLMENT_KIND_LABEL[enrollment.kind] ?? enrollment.kind} · unlocked by {ENROLLMENT_WRAP_LABEL[enrollment.wrap] ?? enrollment.wrap}
                  {enrollment.payload === "legacy-keypair" ? " · pre-seed keys" : ""}
                  {enrollment.created_at ? ` · added ${fmtTime(enrollment.created_at)}` : ""}
                </div>
              </div>
              <Button
                size="sm"
                variant="secondary"
                data-remove-enrollment={enrollment.enrollment_id}
                disabled={Boolean(enrollment.current)}
                aria-label={`Remove ${enrollment.label || enrollment.enrollment_id}`}
                onClick={() => void removeDevice(enrollment.enrollment_id)}
              >
                Remove
              </Button>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-[13px] text-muted">No devices registered yet.</p>
      )}

      <Button
        id="btn-enroll-device"
        className="mt-4"
        onClick={() => {
          closePanel();
          openApproveDevicePanel();
        }}
      >
        Add a device
      </Button>
      {record && !record.seedDerived && (
        <p className="mt-2.5 text-[13px] text-muted">This identity predates recovery kits — see Settings → Recovery kit.</p>
      )}
      <p className="mt-2.5 text-[13px] text-muted">
        Removing a device stops it reading your stored keys and ends its sessions. It does not protect against someone who already copied
        them — that needs a key rotation.
      </p>

      <h3 className="panel-subhead mt-7 mb-1.5 text-[13px] font-semibold tracking-[.04em] text-muted uppercase">Devices using this identity</h3>
      <p className="text-[13px] text-muted">
        What the relay has seen: apps and machines holding sessions, app passwords or sync cursors. Only you can see this list.
      </p>
      {registry === null ? (
        <p className="text-[13px] text-muted">Could not read the device registry.</p>
      ) : registry.length ? (
        <div className="enrollment-list flex flex-col">
          {registry.map((device) => (
            <div key={device.id} className={cn("enrollment-row flex min-h-15 items-center gap-3 border-b border-sep py-3 last:border-b-0", device.revoked && "is-revoked opacity-55")}>
              <span className="enrollment-icon w-8 text-center text-[22px]">{deviceIcon(device.kind)}</span>
              <div className="enrollment-body min-w-0 flex-1">
                <div className="enrollment-label flex flex-wrap items-center gap-1.5 text-[15px] font-semibold">
                  {device.name || "Unnamed device"}
                  {device.revoked && <Chip tone="warning">revoked</Chip>}
                </div>
                <div className="enrollment-meta mt-0.5 text-[13px] text-muted">{describeDevice(device)}</div>
              </div>
              {!device.revoked && (
                <Button
                  size="sm"
                  variant="secondary"
                  data-revoke-device={device.id}
                  aria-label={`Revoke ${device.name || device.id}`}
                  onClick={() => void revokeDevice(device.id)}
                >
                  Revoke
                </Button>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-[13px] text-muted">No devices recorded yet.</p>
      )}
      <p className="mt-2.5 text-[13px] text-muted">
        Revoking ends that device's sessions, DAV tokens and app passwords. A device that still holds your identity key can enrol again —
        that case needs a key rotation.
      </p>
    </div>
  );
}

/**
 * The approving half of the enrollment ceremony (E11-T3). Comparing the six
 * digits *is* the authentication step, so the confirmation is not a formality.
 */
export function openApproveDevicePanel() {
  openPanel("Add a device", () => <ApproveDevice identity={activeIdentity()} />);
}

function ApproveDevice({ identity }: { identity: string }) {
  const code = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<{ enroll: any; request: any } | null>(null);

  const find = async () => {
    const rendezvousId = normalizeRendezvousId(code.current?.value ?? "");
    if (!rendezvousId) {
      toast("Enter the request code from the new device", "warning");
      return;
    }
    const client = requireClient();
    if (!client) return;
    const keys: any = getUnlockedKeys();
    if (!keys?.seed) {
      toast("Only seed-based identities can hand their keys to a new device — see Recovery kit", "warning", 8000);
      return;
    }
    setLoading(true, "Finding the new device…");
    try {
      const { enroll } = await enrollApiForJoin(identity);
      const request = await enroll.pending(client.signer, identity, rendezvousId);
      setLoading(false);
      setPending({ enroll, request });
    } catch (error) {
      setLoading(false);
      setPending(null);
      toast(`No pending device for that request code: ${errorMessage(error)}`, "error", 8000);
    }
  };

  const approve = async () => {
    const client = requireClient();
    const keys: any = getUnlockedKeys();
    if (!client || !pending || !keys?.seed) return;
    setLoading(true, "Sending keys…");
    try {
      await pending.enroll.approve(client.signer, identity, pending.request, fromBase64url(keys.seed));
      setLoading(false);
      closePanel();
      toast("The new device can now finish setting up", "success", 6000);
    } catch (error) {
      setLoading(false);
      toast(errorMessage(error), "error", 8000);
    }
  };

  return (
    <div>
      <ol className="steps mb-3.5 list-decimal pl-5 text-[13px] text-muted">
        <li className="mb-1.5">
          Open this app on the new device and choose <strong>Add this device</strong>.
        </li>
        <li className="mb-1.5">
          It shows a <strong>request code</strong> and a six-digit number.
        </li>
        <li className="mb-1.5">Enter the request code below, then check the six digits match before approving.</li>
      </ol>
      <FormGroup>
        <Label htmlFor="enroll-rendezvous">Request code from the new device</Label>
        <Input
          ref={code}
          id="enroll-rendezvous"
          type="text"
          className="font-mono"
          autoCapitalize="none"
          autoCorrect="off"
          autoComplete="off"
          spellCheck={false}
          placeholder="paste it here"
        />
      </FormGroup>
      <Button id="btn-enroll-lookup" onClick={() => void find()}>
        Continue
      </Button>
      <div id="enroll-confirm">
        {pending && (
          <>
            <Notice className="mt-3.5">
              <p>Confirm this matches the code on the new device:</p>
              <p className="sas-code mt-2.5 mb-1 text-center font-mono text-[34px] font-bold tracking-[.18em]">{pending.request.sas}</p>
              {pending.request.label && <p className="text-[13px] text-muted">{pending.request.label}</p>}
            </Notice>
            <Button id="btn-enroll-approve" onClick={() => void approve()}>
              Codes match — send my keys
            </Button>
          </>
        )}
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
  const { eligible, reason } = recoveryKitEligibility(identity) as { eligible: boolean; reason?: string };
  const keys: any = getUnlockedKeys();
  const [checking, setChecking] = useState(false);
  const [result, setResult] = useState<boolean | null>(null);
  const typed = useRef<HTMLTextAreaElement>(null);

  if (!eligible) {
    return (
      <div>
        <p className="mb-3 text-[13px] text-muted">
          A recovery kit is your identity's master secret written as 24 words. With it you can rebuild this identity anywhere — no relay, no
          email, nothing else to remember.
        </p>
        <Notice tone="warn">
          <strong>Not available for this identity.</strong>{" "}
          {reason === "legacy-keypair"
            ? "It was created with two independent keys rather than from a single seed, so there is no seed to write down. Identities created from now on have one. Converting this one means rotating your keys, which asks every contact to re-pin them — worth it for some people, not for others, so it is offered rather than done for you."
            : "No local record for this identity."}
        </Notice>
      </div>
    );
  }
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

// ─── Relay analytics consent (EPIC-013) ───────────────────────────────────────

export function openAnalyticsPanel() {
  if (!requireClient()) return;
  openPanel("Relay analytics", (close) => <AnalyticsPanel close={close} />);
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
        When your relay exports analytics, timestamps and actions are recorded. With detailed analytics off, your identity is hashed and your
        IP is omitted. Turning it on includes your raw identity and IP. Message contents and keys are never included.
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
              Allow detailed relay analytics for {identity}
            </label>
            <p className="my-2 text-[13px] text-muted">Changes affect future exports. Existing records expire under the relay's retention settings.</p>
            <Button
              id="analytics-save"
              disabled={saving}
              onClick={async () => {
                if (activeIdentity() !== identity) return;
                setSaving(true);
                try {
                  await requireClient()?.setAnalyticsConsent(Boolean(box.current?.checked));
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
