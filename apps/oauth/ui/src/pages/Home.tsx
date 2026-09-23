import { AppWindow, ChevronRight, Code2, History, KeyRound, type LucideIcon } from "lucide-react";
import type { HomeData, Page } from "../lib/page";
import { LinkButton } from "../ui/Button";
import { Card, IconTile, Monogram } from "../ui/Display";
import { Shell } from "../ui/Shell";

export function Home({ page }: { page: Page<HomeData> }) {
  return page.session ? <SignedIn page={page} /> : <SignedOut page={page} />;
}

/** One thing to do: sign in. Everything else lives behind it. */
function SignedOut({ page }: { page: Page<HomeData> }) {
  const launcher = page.data.launcher;
  return (
    <Shell page={page} narrow>
      <div className="animate-fade-in-up pt-6 text-center sm:pt-12">
        <span className="mx-auto mb-6 flex size-16 items-center justify-center rounded-[20px] bg-linear-135 from-accent to-accent-2 text-white shadow-accent">
          <KeyRound className="size-8" strokeWidth={2} aria-hidden="true" />
        </span>
        <h1 className="text-[30px] leading-tight font-extrabold tracking-[-.6px]">Sign in to apps with your Poweur ID</h1>
        <p className="mt-3 text-base text-muted">No passwords. You approve each sign-in on your own device.</p>
        <LinkButton id="home-signin" href="/login?return_to=/" className="mt-8">
          Sign in
        </LinkButton>
        {launcher && (
          <p className="mt-5 text-sm text-muted">
            New to Poweur?{" "}
            <a href={`${launcher.url}/app/`} target="_blank" rel="noopener">
              Create an ID
            </a>
          </p>
        )}
      </div>
    </Shell>
  );
}

function SignedIn({ page }: { page: Page<HomeData> }) {
  const { apps = 0, clients = 0, canRegister } = page.data;
  return (
    <Shell page={page}>
      <div className="mb-8 flex items-center gap-4">
        <Monogram text={page.session!.identity} className="size-14 rounded-full text-2xl" />
        <div className="min-w-0">
          <p className="text-sm text-muted">Signed in as</p>
          <h1 className="truncate text-[22px] font-extrabold tracking-[-.3px]">{page.session!.identity}</h1>
        </div>
      </div>
      <Card className="p-0 sm:p-0">
        <ul className="divide-y divide-sep">
          <Action
            id="home-apps"
            href="/account"
            icon={AppWindow}
            title="Your apps"
            detail={apps ? `${apps} ${apps === 1 ? "app uses" : "apps use"} your Poweur ID — review or revoke` : "Apps you sign in to appear here"}
          />
          <Action id="home-signins" href="/account#sign-ins" icon={History} title="Recent sign-ins" detail="When, where and how you signed in" />
          {(canRegister || clients > 0) && (
            <Action
              id="home-developers"
              href="/developers"
              icon={Code2}
              title="Developer console"
              detail={clients ? `${clients} ${clients === 1 ? "application" : "applications"} you manage` : "Add Poweur sign-in to your application"}
            />
          )}
        </ul>
      </Card>
    </Shell>
  );
}

function Action({ id, href, icon, title, detail }: { id: string; href: string; icon: LucideIcon; title: string; detail: string }) {
  return (
    <li>
      <a id={id} href={href} className="flex items-center gap-3.5 px-5 py-4 text-fg no-underline hover:bg-surface-2/60">
        <IconTile icon={icon} />
        <span className="min-w-0 flex-1">
          <span className="block text-[16px] font-semibold">{title}</span>
          <span className="block truncate text-sm text-muted">{detail}</span>
        </span>
        <ChevronRight className="size-5 text-faint" aria-hidden="true" />
      </a>
    </li>
  );
}
