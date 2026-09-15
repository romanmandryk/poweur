/** The Settings destination (E15-T1…T5), from app.js `renderSettings()`. */
import { useEffect, useState, type ReactNode } from "react";
import { isSessionValid, SDK_BUILD_TIME, SDK_VERSION } from "@poweur/client";
import { loadPolicy, loadProfile } from "../../actions/account";
import { policySummary, removeIdentityFromDevice, rotateEncryptionKey, usesDnsPath } from "../../actions/settings";
import { resetSignInRequest } from "../../actions/signin";
import { APP_BUILD_TIME, APP_VERSION } from "../../build-info";
import { askConfirm } from "../../components/Dialogs";
import { identityApiFor } from "../../lib/client.js";
import { CUSTODY_COPY, custodyOf } from "../../lib/custody";
import { domainOf, handleOf } from "../../lib/identity";
import { defaultRelayUrl, isShellRuntime, loadIdentityRecord, loadSessionRecord, relayUrlFor } from "../../lib/storage.js";
import { useData } from "../../state/data";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { Avatar } from "../../ui/Avatar";
import { Button } from "../../ui/Button";
import { Chip } from "../../ui/Display";
import { DestHeader, SettingsGroup, SettingsRow } from "../../ui/Layout";
import {
  openAnalyticsPanel,
  openConnectedAppsPanel,
  openDnsPanel,
  openIdentityKeysPanel,
  openKeysAndDevicesPanel,
  openLookupPanel,
  openPolicyPanel,
  openProfilePanel,
  openRecoveryKitPanel,
  openRelayPanel,
  openSessionPanel,
} from "./panels";

export function Settings() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const config = useSession((state) => state.config);
  const mode = useSession((state) => state.mode);
  useSession((state) => state.revision); // storage-backed rows below repaint on change
  const policy = useData((state) => state.policy);
  const profile = useData((state) => state.profile);
  const push = useRoute((state) => state.push);

  const record: any = identity ? loadIdentityRecord(identity) : null;
  const session: any = identity ? loadSessionRecord(identity) : null;
  const sessionOk = isSessionValid(session);
  const summary = policySummary(policy);

  useEffect(() => {
    if (!unlocked) return;
    void loadPolicy();
    void loadProfile();
  }, [unlocked, identity]);

  const confirmRemove = async () => {
    if (!identity) return;
    const confirmed = await askConfirm({
      title: "Remove identity",
      message: (
        <>
          <p className="mb-3">
            Remove <strong>{identity}</strong> from this device?
          </p>
          <p className="text-[13px] text-muted">This only removes the local record. Your identity remains registered on the relay and in DNS.</p>
        </>
      ),
      confirmLabel: "Remove from device",
      confirmId: "panel-confirm-remove",
    });
    if (confirmed) removeIdentityFromDevice(identity);
  };

  return (
    <>
      <DestHeader title="Settings" />

      {record && identity ? (
        <div className="settings-id-card flex flex-col items-center gap-2.5 px-5 pt-8 pb-5 text-center">
          <Avatar identity={identity} size="lg" />
          <div className="settings-id-name text-xl font-bold">{handleOf(identity)}</div>
          <div className="settings-id-domain text-sm text-muted">{domainOf(identity)}</div>
          <Chip tone="success" className="mt-1">
            {CUSTODY_COPY[custodyOf(record)].chip}
          </Chip>
        </div>
      ) : (
        <div className="settings-id-card flex flex-col items-center gap-2.5 px-5 pt-8 pb-5 text-center">
          <div className="text-5xl opacity-30">👤</div>
          <p className="text-muted">No identity selected</p>
          <Button id="settings-add-id" size="sm" className="mt-4" onClick={() => push("add-id")}>
            Add identity
          </Button>
        </div>
      )}

      <SettingsGroup label="Account">
        <SettingsRow id="row-switch-id" icon="🔄" label="Switch / Add identity" onClick={() => push("add-id")} />
        {record && (
          <>
            <SettingsRow
              id="row-profile"
              icon="🪞"
              label="Your profile"
              value={profile.doc?.display_name ?? (profile.loaded ? "Not set" : "…")}
              onClick={openProfilePanel}
            />
            <SettingsRow id="row-identity-keys" icon="🪪" label="Identity keys" onClick={openIdentityKeysPanel} />
          </>
        )}
      </SettingsGroup>

      {record && (
        <SettingsGroup label="Security">
          <SettingsRow id="row-keys-devices" icon="📱" label="Keys & devices" onClick={() => void openKeysAndDevicesPanel()} />
          <SettingsRow id="row-connected-apps" icon="🔐" label="Connected apps" onClick={openConnectedAppsPanel} />
          <SettingsRow
            id="row-auth-request"
            icon="✅"
            label="Approve sign-in request"
            onClick={() => {
              resetSignInRequest();
              push("auth");
            }}
          />
          <SettingsRow
            id="row-recovery-kit"
            icon="🧾"
            label="Recovery kit"
            value={record.seedDerived ? "Available" : "Not available"}
            valueTone={record.seedDerived ? "ok" : "warn"}
            onClick={openRecoveryKitPanel}
          />
        </SettingsGroup>
      )}

      <SettingsGroup label="Inbox">
        <SettingsRow id="row-analytics" icon="📊" label="Relay analytics" onClick={openAnalyticsPanel} />
        <SettingsRow id="row-policy" icon="🛡️" label="Who can message you" value={summary.mode} onClick={openPolicyPanel} />
        <SettingsRow
          id="row-policy-anon"
          icon="🎭"
          label="Anonymous & proof-of-work"
          value={summary.anon}
          valueTone={summary.anonOn ? "ok" : undefined}
          onClick={openPolicyPanel}
        />
      </SettingsGroup>

      <SettingsGroup label="Network">
        <SettingsRow id="row-relay" icon="🔗" label="Relay URL" value={config.relayUrl ?? ""} onClick={openRelayPanel} />
        <SettingsRow id="row-lookup" icon="🔎" label="Lookup identity" onClick={openLookupPanel} />
      </SettingsGroup>

      <SettingsGroup label="Advanced">
        <SettingsRow
          id="row-session"
          icon="🔑"
          label="Session"
          value={sessionOk ? "Active" : "None"}
          valueTone={sessionOk ? "ok" : "warn"}
          onClick={session ? openSessionPanel : undefined}
        />
        {usesDnsPath(record, identity, mode.hostedDomains) && (
          <SettingsRow id="row-dns" icon="🌐" label="DNS provider" value={config.dnsProvider || "Cloudflare"} onClick={openDnsPanel} />
        )}
        {record && (
          <>
            <SettingsRow id="row-rotate-enc" icon="🔄" label="Rotate encryption key" onClick={() => void rotateEncryptionKey()} />
            <SettingsRow id="row-remove-id" icon="🗑️" label={<span className="text-danger">Remove this identity</span>} onClick={() => void confirmRemove()} />
          </>
        )}
      </SettingsGroup>

      <SettingsGroup label="About">
        <AboutRow icon="📋" label="Protocol" value="Poweur ID v1" />
        <AboutRow
          icon="💻"
          label="App"
          value={APP_VERSION}
          valueId="about-app-version"
          meta={[APP_BUILD_TIME, isShellRuntime() ? "Mobile" : "Web"].join(" · ")}
          metaId="about-app-build"
        />
        <AboutRow icon="📦" label="SDK" value={SDK_VERSION} valueId="about-sdk-version" meta={SDK_BUILD_TIME} metaId="about-sdk-build" />
        <AboutRelay identity={identity || mode.subject || ""} />
      </SettingsGroup>
      <div className="h-8" />
    </>
  );
}

function AboutRow({
  icon,
  label,
  value,
  meta,
  valueId,
  metaId,
}: {
  icon: string;
  label: string;
  value: ReactNode;
  meta?: ReactNode;
  valueId?: string;
  metaId?: string;
}) {
  return (
    <div className="settings-row settings-row-about no-action flex min-h-13 items-start gap-3 border-b border-sep px-4 py-3.5 last:border-b-0">
      <span className="settings-row-icon w-7 shrink-0 text-center text-xl">{icon}</span>
      <div className="settings-row-stack flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="settings-row-stack-top flex items-baseline justify-between gap-3">
          <span className="settings-row-label text-base">{label}</span>
          <span id={valueId} className="settings-row-value text-right text-[15px] break-all text-muted">
            {value}
          </span>
        </div>
        {(meta || metaId) && (
          <span id={metaId} className="settings-row-meta text-xs leading-snug text-faint [overflow-wrap:anywhere]">
            {meta}
          </span>
        )}
      </div>
    </div>
  );
}

/** The home relay's advertised version, filled in once it answers. */
function AboutRelay({ identity }: { identity: string }) {
  const relayUrl: string = identity ? relayUrlFor(identity) : defaultRelayUrl();
  const [answer, setAnswer] = useState<{ version: string; meta: string } | null>(null);

  useEffect(() => {
    if (!relayUrl) return;
    let live = true;
    (identityApiFor(relayUrl) as any)
      .root()
      .then((root: any) => {
        if (!live) return;
        setAnswer({
          version: root.version || "unknown",
          meta: [identity, relayUrl, root.buildTime, root.versionHash && String(root.versionHash).slice(0, 7)].filter(Boolean).join(" · "),
        });
      })
      .catch(() => live && setAnswer({ version: "—", meta: [identity, relayUrl, "unreachable"].filter(Boolean).join(" · ") }));
    return () => {
      live = false;
    };
  }, [identity, relayUrl]);

  const fallback = relayUrl ? [identity, relayUrl].filter(Boolean).join(" · ") : "Not connected";
  return (
    <AboutRow
      icon="🔗"
      label="Relay"
      value={relayUrl ? (answer?.version ?? "…") : "—"}
      valueId="about-relay-version"
      meta={answer?.meta ?? fallback}
      metaId="about-relay-meta"
    />
  );
}
