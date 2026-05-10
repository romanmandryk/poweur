import { lookupDns } from '../dns/doh';
import { relaySchemeFromUrl } from './routing';

export async function recipientRelayBase(recipient: string, homeRelayUrl: string): Promise<string> {
  const dns = await lookupDns(recipient);
  const host = dns.relayHosts[0] ?? dns.cname;
  if (!host) throw new Error('No relay A/AAAA/CNAME host in DNS for ' + recipient);
  const scheme = relaySchemeFromUrl(homeRelayUrl);
  return `${scheme}://${host}`;
}
