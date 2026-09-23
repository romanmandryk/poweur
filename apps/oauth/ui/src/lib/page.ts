/**
 * The page data the bridge embeds in every HTML response (bridge/web.go) as
 * `<script type="application/json" id="poweur-page">`. Go owns routing,
 * redirects and every security check; this file is the contract for what a
 * page gets to show.
 */
export interface Service {
  name: string;
  issuer: string;
  /** Where "Report" links go: a URL (default /abuse) or mailto. */
  contact: string;
}

export interface Session {
  identity: string;
}

export interface AppRef {
  name: string;
  /** Where it returns to; for URL clients, the verified host. */
  host?: string;
}

export interface Launcher {
  url: string;
  domain: string;
  host: string;
}

export interface HomeData {
  launcher?: Launcher;
  /** Signed in only. */
  apps?: number;
  clients?: number;
  canRegister?: boolean;
}

export interface IdentifyData {
  txn: string;
  client?: AppRef;
  hint?: string;
  error?: string;
  launcher?: Launcher;
}

export interface Signer {
  label: string;
  href: string;
  note?: string;
  /** The identity's own signer (vs. the operator's default). */
  own: boolean;
}

export interface AwaitData {
  txn: string;
  identity: string;
  client?: AppRef;
  signers: Signer[];
  deepLink?: string;
  /** The short link to the request: what the QR shows and "copy" copies. */
  request: string;
  match: string;
  /** Server-drawn SVG of the deep link. */
  qr: string;
  push?: { from: string; sent: number; left: number };
}

/** A phone's camera opened the QR's short link (E08-T6). */
export interface HandoffData {
  identity: string;
  client?: AppRef;
  signers: Signer[];
  deepLink: string;
}

export interface ConsentData {
  txn: string;
  identity: string;
  client: {
    name: string;
    host?: string;
    verifiedHost: boolean;
    registeredBy?: string[];
  };
  optional: string[];
  indieAuth: boolean;
  profileURL?: string;
}

export interface ConsentRow {
  clientId: string;
  clientName: string;
  clientHost?: string;
  granted: string[];
  lastUsed: string;
}

export interface SignInRow {
  at: string;
  clientName?: string;
  userAgent?: string;
  crossDevice: boolean;
  sessionKey: boolean;
}

export interface AccountData {
  consents: ConsentRow[];
  signIns: SignInRow[];
  notice?: string;
}

export interface ClientSummary {
  id: string;
  name: string;
  redirectURIs: string[];
  suspended: boolean;
}

export interface DevelopersData {
  clients: ClientSummary[];
  canWrite: boolean;
  why?: string;
  max: number;
}

export interface ClientForm {
  name: string;
  redirectURIs: string;
  authMethod: string;
  jwks: string;
  jwksURI: string;
  sector: string;
  coOwners: string;
  error?: string;
}

export interface ClientNewData {
  form: ClientForm;
  issuer: string;
}

export interface SecretRow {
  id: string;
  createdAt: string;
  expiresAt?: string;
  status: "active" | "retiring" | "expired";
}

export interface ClientData {
  client: {
    id: string;
    name: string;
    authMethod: string;
    sector: string;
    createdBy?: string;
    createdAt?: string;
    suspended: boolean;
    suspendedReason?: string;
    usesSecret: boolean;
    secrets: SecretRow[];
  };
  form: ClientForm;
  secret?: string;
  issuer: string;
  notice?: string;
}

export interface PolicyData {
  contact?: string;
  securityContact?: string;
  pairwise: boolean;
  audit: string;
  signIns: string;
  consents: string;
  session: string;
  txn: string;
  code: string;
  accessToken: string;
  registration: string;
}

export interface AbuseData {
  contact?: string;
}

export interface ErrorData {
  message: string;
}

export interface Page<T = unknown> {
  page: string;
  title: string;
  service: Service;
  session?: Session;
  data: T;
}

/** Read the page the server embedded; a missing block is a broken deploy. */
export function readPage(doc: Document = document): Page {
  const raw = doc.getElementById("poweur-page")?.textContent;
  if (!raw) {
    return {
      page: "error",
      title: "Something went wrong",
      service: { name: "Poweur sign-in", issuer: "", contact: "/abuse" },
      data: { message: "This page arrived without its content. Reload to try again." },
    };
  }
  return JSON.parse(raw) as Page;
}
