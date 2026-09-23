import type { ReactNode } from "react";
import type { AbuseData, Page, PolicyData } from "../lib/page";
import { Card } from "../ui/Display";
import { PageHead, Shell } from "../ui/Shell";

function Prose({ children }: { children: ReactNode }) {
  return (
    <Card className="space-y-4 text-[15px] leading-relaxed [&_h2]:mt-6 [&_h2]:text-[17px] [&_h2]:font-bold [&_li]:ml-5 [&_li]:list-disc [&_ol_li]:list-decimal [&_ul]:space-y-1.5">
      {children}
    </Card>
  );
}

function Contact({ value, what }: { value?: string; what: string }) {
  if (!value) return <p className="text-muted">The operator has not published a contact yet.</p>;
  const href = value.includes("@") && !value.startsWith("http") ? `mailto:${value}` : value;
  return (
    <p>
      {what}: <a href={href}>{value}</a>
    </p>
  );
}

export function Privacy({ page }: { page: Page<PolicyData> }) {
  const d = page.data;
  return (
    <Shell page={page}>
      <PageHead title="Privacy" subtitle={`${page.service.name} never holds your keys. Here is what it keeps, and for how long.`} />
      <Prose>
        <h2 className="!mt-0">What it keeps</h2>
        <ul>
          <li>A sign-in in progress — the app, the ID you typed and a browser label like “Firefox on macOS” — for {d.txn}, or up to an hour while you create an ID.</li>
          <li>Authorization codes for {d.code}, access tokens for {d.accessToken}. Only their hashes.</li>
          <li>Your session here, for {d.session}. Signing out ends it.</li>
          <li>What you allowed each app to see — until you revoke it, or {d.consents} after last use.</li>
          <li>Recent sign-ins (app, time, browser label), {d.signIns}.</li>
          <li>A security log without content, {d.audit}.</li>
        </ul>
        <h2>What apps learn</h2>
        <p>
          Only what you allow.{" "}
          {d.pairwise
            ? "Each app gets its own identifier for you, so apps can't match you up; your Poweur ID is shared only if you allow it."
            : "Every app gets the same identifier for you."}
        </p>
        <h2>What it doesn't do</h2>
        <ul>
          <li>No ads, analytics or third-party scripts. The one exception: choosing a name for a new ID asks the ID provider if it's free.</li>
          <li>No passwords and no private keys.</li>
          <li>No stored IP addresses — they're used briefly, in memory, to limit request rates.</li>
        </ul>
        <h2>Contact</h2>
        <Contact value={d.contact} what="Questions about your data" />
      </Prose>
    </Shell>
  );
}

export function Security({ page }: { page: Page<PolicyData> }) {
  const d = page.data;
  return (
    <Shell page={page}>
      <PageHead title="Security" subtitle="Reporting a problem, and what happens when something goes wrong." />
      <Prose>
        <h2 className="!mt-0">Report a vulnerability</h2>
        <Contact value={d.securityContact} what="Write to" />
        <p>Test only with your own identities and apps, don't access other people's data, and give us time to fix it before publishing.</p>
        <h2>Built to fail safely</h2>
        <ul>
          <li>It holds no user keys: a compromise can't sign as you anywhere else.</li>
          <li>Signing keys are stored encrypted and rotated.</li>
          <li>A sign-in finishes only in the browser that started it, or one you confirmed with its code.</li>
          <li>A reused authorization code revokes everything issued from it.</li>
        </ul>
        <h2>If something goes wrong</h2>
        <ol>
          <li>Contain: suspend affected apps, rotate keys, or take the service offline.</li>
          <li>Assess what was affected, from the security log.</li>
          <li>Tell affected people and developers within 72 hours of confirming an incident that exposed personal data.</li>
          <li>Fix, review, and publish what changed.</li>
        </ol>
        <p className="text-muted">
          App registration here is <strong className="text-fg">{d.registration}</strong>. Misleading app? <a href="/abuse">Report it</a>.
        </p>
      </Prose>
    </Shell>
  );
}

export function Abuse({ page }: { page: Page<AbuseData> }) {
  return (
    <Shell page={page}>
      <PageHead title="Report an app" subtitle="An app misled you on a sign-in page? Tell the operator." />
      <Prose>
        <Contact value={page.data.contact} what="Contact" />
        <h2>Include</h2>
        <ul>
          <li>The app's name as the consent page showed it.</li>
          <li>Its client ID — in the address bar as <code>client_id</code>.</li>
          <li>Where you were sent afterwards, and when.</li>
        </ul>
        <h2>What happens</h2>
        <p>
          The operator may suspend the app, which stops every sign-in to it at once. Approved something you regret? Revoke it in{" "}
          <a href="/account">your apps</a>.
        </p>
      </Prose>
    </Shell>
  );
}
