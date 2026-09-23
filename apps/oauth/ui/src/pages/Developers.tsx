import { useState } from "react";
import { ChevronRight, Code2, KeyRound, Plus } from "lucide-react";
import { cn } from "../lib/cn";
import { formatDate } from "../lib/format";
import type { ClientData, ClientForm, ClientNewData, DevelopersData, Page } from "../lib/page";
import { Button, LinkButton } from "../ui/Button";
import { Banner, Card, Chip, CopyValue, Disclosure, Monogram } from "../ui/Display";
import { ErrorLine, Field, Input, Textarea } from "../ui/Field";
import { PageHead, Shell } from "../ui/Shell";
import { Empty } from "./Account";

export function Developers({ page }: { page: Page<DevelopersData> }) {
  const d = page.data;
  return (
    <Shell page={page}>
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <PageHead className="mb-0" title="Developer console" subtitle="Add Poweur sign-in to any OpenID Connect application." />
        {d.canWrite && d.clients.length > 0 && (
          <LinkButton id="dev-new" href="/developers/new" size="sm" className="w-auto">
            <Plus className="size-4" aria-hidden="true" /> New application
          </LinkButton>
        )}
      </div>
      <Card className="p-0 sm:p-0">
        {d.clients.length ? (
          <ul id="dev-clients" className="divide-y divide-sep">
            {d.clients.map((c) => (
              <li key={c.id}>
                <a href={`/developers/clients/${c.id}`} className="flex items-center gap-3.5 px-5 py-4 text-fg no-underline hover:bg-surface-2/60">
                  <Monogram text={c.name} className="size-10 rounded-[12px] text-base" />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2 font-semibold">
                      {c.name} {c.suspended && <Chip tone="danger">suspended</Chip>}
                    </span>
                    <span className="block truncate font-mono text-[12px] text-muted">{c.id}</span>
                  </span>
                  <ChevronRight className="size-5 text-faint" aria-hidden="true" />
                </a>
              </li>
            ))}
          </ul>
        ) : (
          <div className="p-6">
            <Empty icon={<Code2 className="size-7" aria-hidden="true" />} text="No applications yet." />
            {d.canWrite && (
              <LinkButton id="dev-new" href="/developers/new" className="mx-auto max-w-xs">
                <Plus className="size-5" aria-hidden="true" /> Register an application
              </LinkButton>
            )}
          </div>
        )}
      </Card>
      <p className="mt-3 px-1 text-[13px] text-muted">
        {d.canWrite ? `Up to ${d.max} applications per Poweur ID.` : d.why} Issuer: <code className="font-mono">{page.service.issuer}</code>
      </p>
    </Shell>
  );
}

function ClientFields({ form }: { form: ClientForm }) {
  return (
    <>
      <Field label="Name" htmlFor="name" hint="People see it when they sign in, with your Poweur ID beside it.">
        <Input id="name" name="name" type="text" maxLength={80} required defaultValue={form.name} placeholder="My app" />
      </Field>
      <Field label="Redirect URIs" htmlFor="redirect_uris" hint="One per line. Exact match; https, or http for 127.0.0.1 and [::1].">
        <Textarea
          id="redirect_uris"
          name="redirect_uris"
          rows={3}
          required
          defaultValue={form.redirectURIs}
          placeholder="https://app.example.com/oauth2/callback"
          className="font-mono text-sm"
        />
      </Field>
    </>
  );
}

function CoOwners({ form }: { form: ClientForm }) {
  return (
    <Field label="Co-owners" htmlFor="co_owners" hint="Other Poweur IDs that may manage it, one per line.">
      <Textarea id="co_owners" name="co_owners" rows={2} defaultValue={form.coOwners} placeholder="colleague.poweur.net" />
    </Field>
  );
}

const METHODS = [
  { value: "client_secret_basic", title: "Client secret", detail: "Keycloak, Authentik, oauth2-proxy, most servers" },
  { value: "none", title: "No secret (PKCE)", detail: "Browser or mobile apps" },
  { value: "private_key_jwt", title: "Signed JWT", detail: "Your own key pair" },
  { value: "client_secret_post", title: "Secret in form body", detail: "Servers that can't send Basic auth" },
];

export function ClientNew({ page }: { page: Page<ClientNewData> }) {
  const form = page.data.form;
  const [method, setMethod] = useState(form.authMethod || "client_secret_basic");
  return (
    <Shell page={page}>
      <p className="mb-2 text-sm">
        <a href="/developers">← Developer console</a>
      </p>
      <PageHead title="Register an application" />
      <Card>
        <form method="post" action="/developers/new">
          <ClientFields form={form} />
          <fieldset className="mb-4">
            <legend className="mb-1.5 text-[13px] font-semibold text-muted">How it authenticates</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {METHODS.map((m) => (
                <label
                  key={m.value}
                  className={cn(
                    "flex cursor-pointer items-start gap-3 rounded-control border-[1.5px] px-3.5 py-3",
                    method === m.value ? "border-accent bg-accent-soft" : "border-sep",
                  )}
                >
                  <input
                    type="radio"
                    name="auth_method"
                    value={m.value}
                    checked={method === m.value}
                    onChange={() => setMethod(m.value)}
                    className="mt-1 accent-accent"
                  />
                  <span>
                    <span className="block text-[15px] font-semibold">{m.title}</span>
                    <span className="block text-[13px] text-muted">{m.detail}</span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
          {method === "private_key_jwt" && (
            <>
              <Field label="Public JWK set" htmlFor="jwks">
                <Textarea id="jwks" name="jwks" rows={4} defaultValue={form.jwks} className="font-mono text-sm" />
              </Field>
              <Field label="…or a JWKS URL" htmlFor="jwks_uri">
                <Input id="jwks_uri" name="jwks_uri" type="url" defaultValue={form.jwksURI} />
              </Field>
            </>
          )}
          <Disclosure summary="More options" className="mb-4">
            <CoOwners form={form} />
            <Field label="Subject sector" htmlFor="sector" hint="Apps sharing a sector see the same user identifier. Fixed after creation.">
              <Input id="sector" name="sector" type="text" defaultValue={form.sector} placeholder="Your first redirect URI's host" />
            </Field>
          </Disclosure>
          <ErrorLine className="mb-3">{form.error}</ErrorLine>
          <Button type="submit" id="client-register">
            Register
          </Button>
        </form>
      </Card>
    </Shell>
  );
}

const METHOD_TITLE = Object.fromEntries(METHODS.map((m) => [m.value, m.title]));

export function ClientDetail({ page }: { page: Page<ClientData> }) {
  const d = page.data;
  const c = d.client;
  const [confirm, setConfirm] = useState("");
  return (
    <Shell page={page}>
      <p className="mb-2 text-sm">
        <a href="/developers">← Developer console</a>
      </p>
      <div className="mb-6 flex items-center gap-4">
        <Monogram text={c.name} className="size-14 text-2xl" />
        <div className="min-w-0">
          <h1 className="truncate text-[26px] font-extrabold tracking-[-.4px]">{c.name}</h1>
          <p className="text-sm text-muted">
            {c.createdBy && `Created by ${c.createdBy}`}
            {c.createdAt && `, ${formatDate(c.createdAt)}`}
          </p>
        </div>
      </div>
      {d.notice && <Banner className="mb-4">{d.notice}</Banner>}
      {c.suspended && (
        <Banner tone="danger" className="mb-4">
          Suspended by the operator{c.suspendedReason ? `: ${c.suspendedReason}` : ""}.
        </Banner>
      )}

      {d.secret && (
        <Card id="new-secret" className="mb-4 border-[1.5px] border-success/40">
          <p className="mb-2 flex items-center gap-2 font-semibold">
            <KeyRound className="size-4 text-success" aria-hidden="true" /> Client secret — copy it now, it won't be shown again
          </p>
          <CopyValue id="client-secret" value={d.secret} label="Copy secret" />
        </Card>
      )}

      <Card className="mb-4">
        <h2 className="mb-4 text-[17px] font-bold">Credentials</h2>
        <dl className="grid gap-4 text-sm sm:grid-cols-[140px_1fr] sm:items-center">
          <dt className="font-semibold text-muted">Issuer</dt>
          <dd>
            <CopyValue value={d.issuer} />
          </dd>
          <dt className="font-semibold text-muted">Client ID</dt>
          <dd>
            <CopyValue id="client-id" value={c.id} />
          </dd>
          <dt className="font-semibold text-muted">Authentication</dt>
          <dd>{METHOD_TITLE[c.authMethod] ?? c.authMethod}</dd>
          <dt className="font-semibold text-muted">Scopes</dt>
          <dd>
            <code className="font-mono">openid</code>, optional <code className="font-mono">poweur_id</code> and <code className="font-mono">profile</code>
          </dd>
          <dt className="font-semibold text-muted">Sector</dt>
          <dd className="font-mono">{c.sector}</dd>
        </dl>
      </Card>

      <Card className="mb-4">
        <h2 className="mb-4 text-[17px] font-bold">Settings</h2>
        <form method="post" action={`/developers/clients/${c.id}`}>
          <ClientFields form={d.form} />
          <CoOwners form={d.form} />
          <ErrorLine className="mb-3">{d.form.error}</ErrorLine>
          <Button type="submit" id="client-save" size="sm">
            Save changes
          </Button>
        </form>
      </Card>

      {c.usesSecret && (
        <Card className="mb-4">
          <h2 className="mb-1 text-[17px] font-bold">Secrets</h2>
          <p className="mb-4 text-[13px] text-muted">A new secret keeps the old ones working for 7 days, so you can roll it out.</p>
          <ul className="mb-4 divide-y divide-sep rounded-control bg-surface-2">
            {c.secrets.map((s) => (
              <li key={s.id} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                <code className="font-mono">…{s.id}</code>
                <span className="text-muted">{formatDate(s.createdAt)}</span>
                <span className="flex-1">
                  {s.status === "active" ? (
                    <Chip tone="success">active</Chip>
                  ) : s.status === "retiring" ? (
                    <Chip tone="warning">stops {formatDate(s.expiresAt)}</Chip>
                  ) : (
                    <Chip>expired</Chip>
                  )}
                </span>
                <form method="post" action={`/developers/clients/${c.id}/secrets/${s.id}/retire`}>
                  <button type="submit" className="font-semibold text-danger">
                    Retire
                  </button>
                </form>
              </li>
            ))}
          </ul>
          <form method="post" action={`/developers/clients/${c.id}/secrets`}>
            <Button type="submit" variant="secondary" size="sm" id="client-rotate">
              Create a new secret
            </Button>
          </form>
        </Card>
      )}

      <Card>
        <h2 className="mb-1 text-[17px] font-bold text-danger">Delete</h2>
        <p className="mb-4 text-[13px] text-muted">The application stops working at once.</p>
        <form method="post" action={`/developers/clients/${c.id}/delete`} className="flex flex-col gap-3 sm:flex-row">
          <label htmlFor="confirm" className="sr-only">
            Type the client ID to confirm
          </label>
          <Input
            id="confirm"
            name="confirm"
            type="text"
            autoComplete="off"
            placeholder={`Type ${c.id}`}
            value={confirm}
            onChange={(e) => setConfirm(e.currentTarget.value)}
            className="font-mono text-sm"
          />
          <Button type="submit" variant="danger" disabled={confirm !== c.id} id="client-delete" className="shrink-0">
            Delete
          </Button>
        </form>
      </Card>
    </Shell>
  );
}
