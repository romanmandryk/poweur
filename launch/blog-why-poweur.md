---
title: "We were supposed to own our place on the internet"
description: "Why I'm building Poweur: an open identity, an inbox and a home for your data, for an internet where people and agents can work together across company walls."
author: Roman Mandryk
date: 2026-10-13
draft: true
---

# We were supposed to own our place on the internet

Working together online still begins with choosing whose platform everyone has to join. A small project needs a chat app, somewhere for files, invitations and permissions. Add an AI assistant and there is another set of connections to configure.

Our tools keep improving, but our identities, relationships and work remain scattered across company accounts. I want those things to last longer than our choice of software. That's why I'm building Poweur.

## How we became tenants

Early computer identities were usernames on individual machines. Networked email connected them: in 1971, Ray Tomlinson introduced the system that put `@` between a user and their host, making people reachable across computers. [The Internet Hall of Fame recounts that history](https://www.internethalloffame.org/2012/07/30/meet-man-who-put-your-e-mail/).

Forums and chat networks brought more handles. The web brought personal sites and domain names: addresses people could visit without joining the same service. Open protocols let independently operated systems participate in one network.

Then every application wanted its own account. “Sign in with Google” and similar services reduced the friction by letting an existing account vouch for us elsewhere. That convenience also concentrated our dependence on a few companies, while our conversations and work accumulated inside their platforms.

Underneath much of this arrangement, identity still rests on email addresses and phone numbers. We use them to establish accounts, authenticate and recover access, although receiving something at an address is a limited way to prove who we are.

The weaknesses are well documented. NIST's digital identity guidance disallows email for out-of-band authentication and restricts telephone-network authentication, noting risks including SIM changes and number porting. These are limitations of using delivery channels as authenticators, rather than a claim that every mailbox or phone is compromised. [NIST explains the distinction](https://pages.nist.gov/800-63-4/sp800-63b/authenticators/).

Email showed that different providers could communicate through an open standard. We should build on that achievement while giving identity a stronger foundation.

## Three foundations under our control

Most online activity involves three things: establishing who is acting, exchanging messages, and working on shared data.

Each platform rebuilds these foundations with its own accounts, inboxes, storage and permissions. Trying a better tool often means moving our data and persuading other people to move too.

I want to be able to change applications without rebuilding my relationships. I want what I create to remain useful after its editor changes direction. Anyone should be able to build a service that connects to the rest of the internet through shared standards.

Poweur starts with a readable identity, such as `alice.poweur.net` or a name on your own domain, backed by cryptographic keys you control. It connects that identity to signed, end-to-end encrypted messaging and a home for files and sharing. Its sign-in bridge lets applications configured for OAuth or OpenID Connect accept a Poweur identity.

The foundations are domain names, HTTPS, public-key cryptography and open protocols. The work is bringing them together in a useful everyday experience.

Control has practical limits. A hosted name depends on whoever controls its domain. Your own domain gives you more control over the name, while hosting and recovery remain responsibilities somebody must handle. Open software gives you choices about who provides those services.

## A common workspace for people and agents

Agents make the need for open foundations more urgent. Preparing a proposal might require a brief, a discussion, a price list and somewhere to leave a draft. Across closed applications, each step can require another connector, permission model and translation of the same information.

A shared folder offers a simpler starting point. The brief is Markdown, the price list is CSV, and the assistant leaves its draft alongside the source material. A person opens it in their preferred editor. Both work on the same information.

Text files are easy to read, search, compare and move. Agents can process them directly, and people can inspect the results without adopting a particular vendor's tools. Open, documented formats let the work survive changes in software.

Files still need permissions, versioning and ways to handle simultaneous edits. Agents need clear boundaries too. The direction I want for Poweur is to give an agent its own identity, limit its access to the job, and make its actions attributable and its future access revocable.

Poweur is pre-1.0. Identity, messaging, files, sharing and sign-in are implemented; the broader agent experience, app ecosystem and public discovery layer are work ahead. An external security audit is also still ahead. You can [read the code and follow development](https://github.com/romanmandryk/poweur).

## Most of life online, between peers

Families sharing plans, clubs organising events and businesses working with customers should be able to collaborate through identities and services they choose. People and their agents should be able to exchange messages and documents across providers.

Poweur currently uses federated relays. Peer-to-peer in this vision includes services acting on our behalf: hosting, backups and dependable delivery remain useful things to pay for. Everyone need not operate their own server.

Discovery benefits from a broader view. Finding a tradesperson, following a subject or searching public information needs indexers and aggregators. We might each use a handful of relatively large indexes alongside smaller ones for a neighbourhood, profession or interest.

Specialist businesses and communities could operate these services without owning everyone's identity, private conversations or original work. A directory could introduce a customer to a business. A community could choose its own feed and moderation rules. Another index could offer a different view of the same public material.

That is the internet I want to help build: most of our online life and work happening between peers, supported by a few useful layers of discovery. Small businesses and communities could run the parts they understand best. We could choose big tech when it offers something worthwhile, with a practical way to live and work without depending on it.
