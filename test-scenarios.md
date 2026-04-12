# Eurything Test Scenarios

This document defines the end-to-end scenarios needed to validate the Eurything system across the relay, CLI, documentation, infrastructure, and mobile apps.

It is intentionally split by automation level:

- `Automated`: should run in CI without human involvement.
- `Mostly automated`: can run in CI or a scripted environment, but may require external DNS credentials or ephemeral infrastructure.
- `Manual`: requires a human, usually because of passkey or mobile platform approval UX.

## Scope And Assumptions

Current repository status matters for planning:

- `apps/api` is only a minimal HTTP stub today.
- `apps/cli` is still a placeholder.
- `apps/ios` and `apps/android` are scaffolds, not full native apps yet.

The scenarios below are therefore acceptance targets for the intended system, not a claim that the current repo already passes them.

## Test Environments

### Local automated environment

Use for relay, CLI, docs, and protocol tests.

- Two relay instances running locally on different ports
- A local DNS resolver or test DNS server (for example CoreDNS) with programmable records
- Two test identities such as `alice.test` and `bob.test`
- One verifier test app that simulates third-party sign-up and sign-in requests

### Provider-backed integration environment

Use for DNS provider and infra validation.

- Cloudflare test zone
- Hetzner DNS test zone
- Optional Hetzner Cloud staging project

### Manual device lab

Use for native mobile and passkey validation.

- One iPhone with biometrics enabled
- One Android device with biometrics enabled
- Desktop browser for QR-based auth flow
- Test websites or apps acting as relying parties

## Scenario Matrix

| ID | Area | Scenario | Automation |
|----|------|----------|------------|
| DOC-01 | Docs | Build the Docusaurus docs successfully | Automated |
| DOC-02 | Docs | Verify all internal doc links and sidebar entries resolve | Automated |
| API-01 | Relay | `GET /health` returns `200` and protocol version metadata | Automated |
| API-02 | Relay | `POST /identities` writes public key and routing DNS records | Mostly automated |
| API-03 | Relay | Invalid DNS token returns provider write failure | Mostly automated |
| API-04 | Relay | Valid signed local message is accepted and stored for recipient | Automated |
| API-05 | Relay | Invalid signature is rejected with `401` | Automated |
| API-06 | Relay | Oversized payload is rejected with `413` before verification | Automated |
| API-07 | Relay | Rate limits trigger `429` before expensive work | Automated |
| API-08 | Relay | Remote message routing works across two relays using DNS lookup | Automated |
| API-09 | Relay | DNS cache honors TTL expiry and refreshes routing | Automated |
| API-10 | Relay | `GET /auth/challenge` issues a short-lived challenge and invalidates it after use | Automated |
| API-11 | Relay | `GET /messages/:identity` returns inbox after valid signed challenge | Automated |
| CLI-01 | CLI | Create local identity and output machine-readable JSON | Automated |
| CLI-02 | CLI | Show current identity and public key | Automated |
| CLI-03 | CLI | Send a signed message through the relay | Automated |
| CLI-04 | CLI | Read inbox through challenge-response flow | Automated |
| CLI-05 | CLI | Sign third-party verifier request in headless mode | Automated |
| AUTH-01 | Auth | Website sign-up request is approved in mobile app via QR | Manual |
| AUTH-02 | Auth | Website sign-in request is approved in mobile app via QR | Manual |
| AUTH-03 | Auth | Same-device deep-link sign-in works from mobile browser to app and back | Manual |
| AUTH-04 | Auth | Expired or replayed auth request is rejected by verifier | Automated |
| AUTH-05 | Auth | Verifier can resolve identity from DNS and validate signature | Automated |
| AUTH-06 | Auth | Verifier can consume `/.well-known/eurything.json` metadata | Automated |
| AUTH-07 | Auth | Optional `did:web` document is served and matches DNS key material | Automated |
| MOBILE-01 | iOS | Create identity with passkey-backed keypair and verify DNS propagation | Manual |
| MOBILE-02 | Android | Create identity with passkey-backed keypair and verify DNS propagation | Manual |
| MOBILE-03 | iOS | Approve third-party sign-up request and return signed response | Manual |
| MOBILE-04 | Android | Approve third-party sign-in request and return signed response | Manual |
| MOBILE-05 | Mobile | Send and receive messages between two mobile identities | Manual |
| INFRA-01 | Infra | Terraform plan succeeds with staging variables | Automated |
| INFRA-02 | Infra | Terraform apply creates relay host, LB, DNS, and TLS resources in staging | Mostly automated |
| INFRA-03 | Infra | Deployed relay passes health check behind load balancer | Mostly automated |

## Detailed Scenarios

### DOC-01: Documentation build

Goal: keep the docs site as a reliable source of truth.

Steps:

1. Install workspace dependencies.
2. Build the docs site in `apps/docs`.
3. Fail if Markdown frontmatter, sidebar registration, or imports are invalid.

Expected result:

- Build completes successfully.

### API-02: Identity registration writes DNS

Goal: prove that relay registration can create the minimum viable identity records.

Steps:

1. Start relay with test DNS provider credentials or a programmable fake provider.
2. Submit `POST /identities` with a new identity and public key.
3. Query DNS for `_eurything.<identity>` and `<identity>`.

Expected result:

- TXT public key record exists and matches the submitted public key.
- Routing record exists and points at the expected relay address.

### API-08: Cross-relay message routing

Goal: prove that routing is DNS-driven rather than registry-driven.

Steps:

1. Start relay A and relay B.
2. Register `alice.test` on relay A and `bob.test` on relay B.
3. Send a signed message from Alice to Bob via relay A.
4. Fetch Bob's inbox from relay B.

Expected result:

- Relay A resolves Bob through DNS.
- Relay B verifies Alice's signature independently.
- Bob receives the original signed envelope unchanged.

### CLI-05: Headless auth signing

Goal: support bot and agent participation in third-party auth flows.

Steps:

1. Create or load a CLI-managed identity.
2. Provide a verifier request object to the CLI.
3. Have the CLI inspect and sign the request.
4. Submit the signed result to the verifier test app.

Expected result:

- CLI outputs a signed auth response in JSON.
- Verifier accepts the response and ties it to the original nonce and expiry.

### AUTH-01 / AUTH-02: Third-party sign-up and sign-in

Goal: prove that the DNS identity is usable beyond messaging.

Steps:

1. Open a relying-party website in a desktop browser.
2. Start a sign-up or sign-in flow that displays a QR code.
3. Scan the QR code with the Eurything mobile app.
4. Review verifier domain, requested action, and statement.
5. Approve with biometrics or passkey confirmation.
6. Return to the website and verify completion.

Expected result:

- The website receives a signed response for the selected identity.
- Signature verification succeeds against DNS-published key material.
- Replay of the same signed request is rejected.

### AUTH-07: `did:web` compatibility

Goal: ensure Eurything can interoperate with DID-aware systems without changing its canonical identifier.

Steps:

1. Publish `https://<identity>/.well-known/did.json`.
2. Resolve the DID document.
3. Compare the verification method in the DID document with the key published in DNS.
4. Verify the same signed auth response with both representations.

Expected result:

- DNS and DID document expose equivalent verification material.
- Both verification paths accept the same legitimate signature.

## Recommended CI Split

Run these groups separately:

- `docs`: docs build and link checks
- `protocol-unit`: canonical signing, verification, envelope parsing
- `relay-e2e`: local relay plus local DNS scenarios
- `cli-e2e`: CLI plus relay integration
- `provider-integration`: Cloudflare and Hetzner staging runs
- `mobile-manual`: iOS and Android checklist-driven manual validation

## Exit Criteria

The system is ready for serious external trials when:

- all automated relay and CLI scenarios pass consistently
- provider-backed integration succeeds for at least one supported DNS provider
- both mobile platforms complete identity creation and auth approval flows manually
- the third-party sign-in flow is validated on both QR-based and deep-link based paths
