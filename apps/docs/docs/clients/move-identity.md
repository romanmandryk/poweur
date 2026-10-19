---
id: move-identity
title: Move an ID to another machine
---

# Move an ID to another machine

A Poweur ID is its keys. Whoever has them can act as the ID, and the relay holds only the public
half, so moving an ID means getting the private keys onto the new machine. There are three ways.
Pick one by what you have.

| You have | Use |
|---|---|
| The old machine, still working | [Copy the Poweur folder](#copy-the-poweur-folder) |
| The 24-word recovery phrase | [Restore from the recovery phrase](#restore-from-the-recovery-phrase) |
| The ID in the Poweur app (your own ID) | [Pair the new machine](#pair-the-new-machine) |
| Neither keys nor phrase | The ID cannot be recovered: nobody can reset it, by design. Create a new one |

Agent IDs (for example an [OpenClaw agent](/agents/openclaw)) move the same way. Stop the agent on
the old machine first, so two machines do not answer the same messages.

## Copy the Poweur folder

The keys live in the Poweur folder: `~/.poweur` (or wherever `POWEUR_HOME` points), in
`keys/<id>.key` and `keys/<id>.enc`, next to `config.toml`.

```bash
# on the old machine
tar czf poweur-home.tgz -C ~ .poweur
scp poweur-home.tgz new-machine:
```

```bash
# on the new machine
tar xzf poweur-home.tgz -C ~ && rm poweur-home.tgz
npx @poweur/client identity show
```

The archive holds the private keys: move it over an encrypted channel and delete it afterwards.
If the keys are protected with a passphrase (`poweur key protect`, Go CLI), the same passphrase
unlocks them on the new machine; protected keys need the Go CLI, the Node CLI and the OpenClaw
plugin read only unprotected ones.

## Restore from the recovery phrase

The recovery phrase was shown once when the ID was created (and, for an OpenClaw agent, saved
next to its keys in `~/.poweur/recovery/`). Put it in a file only you can read, then:

```bash
npx @poweur/client key recover alice.poweur.net --seed-file ~/alice-phrase.txt
rm ~/alice-phrase.txt
```

The Node CLI checks the phrase against the keys the ID publishes and saves nothing if they do not
match. With the Go binary: `poweur key recover alice.poweur.net --seed "$(cat ~/alice-phrase.txt)"`.
For an OpenClaw agent, the setup command does it in one go:

```bash
openclaw channels add --channel poweur --identity alice-claw.poweur.net --allow-from alice.poweur.net --recovery-phrase-file ~/agent-phrase.txt
```

## Pair the new machine

For your own ID, held in the Poweur app: start pairing on the new machine and approve it in the
app (Settings → Keys & devices), comparing the digits both sides show.

```bash
npx @poweur/client key enroll alice.poweur.net
```

The approving side delivers the seed, encrypted to the new machine, and the new machine saves it
only if the keys match the ID. See [Pairing a new device](/security/key-management#pairing-a-new-device).

## Afterwards: the old machine

The keys are now on both machines, and both can act as the ID.

- **Old machine retired, and you trust it:** delete its keys, `rm ~/.poweur/keys/alice.poweur.net.*`.
- **Old machine lost, stolen or not trusted:** delete is not enough, because a copy may exist.
  Move the ID onto new keys with `poweur key rotate` on the new machine. The old keys stop working
  after the grace period (default 7 days), and your other devices must be paired again. Contacts
  follow the rotation without warnings.
