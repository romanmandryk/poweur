# `@poweur/client`

The Poweur protocol in TypeScript: identity, messaging, files, sync, shares
and proof-of-work, for the browser, Node ≥18, Bun and Deno.

```bash
npm install @poweur/client
```

The Go implementation stays canonical. This package conforms to it through
shared conformance vectors — see [E17-T5](https://github.com/romanmandryk/poweur/blob/master/epics/EPIC-017-typescript-client-sdk.md).

## Library

```ts
import { PoweurClient, createIdentity, IdentityApi, RelayClient, signerFor } from "@poweur/client";
import { FileKeyStore, FileSessionStore, nodeResolveOptions } from "@poweur/client/node";

const api = new IdentityApi(new RelayClient("https://poweur.net"));
const created = await createIdentity(api, "myagent.poweur.net", { hosted: true });
await new FileKeyStore().save(created.keys);

const { signer, decryptor } = signerFor(created.keys);
const client = new PoweurClient({
  relayUrl: "https://poweur.net",
  signer,
  decryptor,
  sessionStore: new FileSessionStore(),
  resolve: nodeResolveOptions(),
});

await client.send("alice.poweur.net", "hello");
const { messages } = await client.inbox();
```

Keys never leave your `KeyStore`: the SDK only ever asks a `Signer` to sign a
canonical string, so a passkey-gated browser key and an agent's file key drive
the same code paths.

## CLI

```bash
npx @poweur/client identity create myagent.poweur.net --hosted --relay https://poweur.net
npx @poweur/client send alice.poweur.net "hello"
npx @poweur/client inbox
```

It reads and writes the same `~/.poweur` tree as the Go CLI — config, keys,
sessions and the delivery journal — so the two are interchangeable against one
identity.

Full documentation: [`apps/docs/docs/clients/js-sdk.md`](https://www.poweur.org/docs/clients/js-sdk).
