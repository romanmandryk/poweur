import { decodeAnyBase64 } from '../util/base64';

export type DNSStatus = {
  identity: string;
  publicKeyTXT: string;
  encryptionKeyTXT: string;
  relayHosts: string[];
  cname: string;
};

type DoHAnswer = { data: string; type: number };

function stripTxt(data: string): string {
  let s = data.trim();
  if (s.startsWith('"')) {
    s = s.replace(/^"(.*)"$/s, '$1').replace(/" "/g, '');
  }
  return s;
}

async function queryDoH(name: string, type: string): Promise<DoHAnswer[]> {
  const url = new URL('https://cloudflare-dns.com/dns-query');
  url.searchParams.set('name', name);
  url.searchParams.set('type', type);
  const r = await fetch(url.toString(), { headers: { accept: 'application/dns-json' } });
  if (!r.ok) throw new Error(`DoH ${type} failed: ${r.status}`);
  const j = (await r.json()) as { Answer?: DoHAnswer[] };
  return j.Answer ?? [];
}

async function lookupHost(name: string): Promise<string[]> {
  const out: string[] = [];
  for (const t of ['A', 'AAAA'] as const) {
    const ans = await queryDoH(name, t);
    for (const a of ans) {
      const v = stripTxt(a.data);
      if (v && !out.includes(v)) out.push(v);
    }
  }
  return out;
}

export async function lookupDns(identity: string): Promise<DNSStatus> {
  const status: DNSStatus = {
    identity,
    publicKeyTXT: '',
    encryptionKeyTXT: '',
    relayHosts: [],
    cname: '',
  };
  const pkAns = await queryDoH('_eurything.' + identity, 'TXT');
  for (const a of pkAns) {
    const raw = stripTxt(a.data);
    if (raw.includes('eurything-pubkey=')) {
      status.publicKeyTXT = raw;
      break;
    }
  }
  const encAns = await queryDoH('_eurything-enc.' + identity, 'TXT');
  for (const a of encAns) {
    const raw = stripTxt(a.data);
    if (raw.includes('eurything-enckey=')) {
      status.encryptionKeyTXT = raw;
      break;
    }
  }
  const cnameAns = await queryDoH(identity, 'CNAME');
  if (cnameAns.length > 0) {
    status.cname = stripTxt(cnameAns[0]!.data).replace(/\.$/, '');
  }
  status.relayHosts = await lookupHost(identity);
  return status;
}

export function parseEncryptionKeyFromTxt(txt: string): Uint8Array | null {
  const line = txt.trim();
  if (!line.startsWith('eurything-enckey=')) return null;
  let v = line.slice('eurything-enckey='.length).trim();
  if (v.startsWith('x25519:')) v = v.slice('x25519:'.length);
  try {
    const b = decodeAnyBase64(v);
    return b.length === 32 ? b : null;
  } catch {
    return null;
  }
}
