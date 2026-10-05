---
title: "Introducing Poweur ID: your name, your inbox, your data"
description: "What if your internet ID was a domain name, and your apps and agents worked with data you own?"
author: Roman Mandryk
date: 2026-10-05
---

# Introducing Poweur ID: your name, your inbox, your data

Most of our lives online begin with an email address, a phone number or an account with a big tech company. We use them to sign in, find each other and share things, but each app builds its own little world around us. Our conversations, documents and relationships end up scattered across isolated services. We can often download a copy, yet we have surprisingly little control over how that data moves between the tools we use.

I started building Poweur ID around a simple question: domain names and URLs are already everywhere on the internet, so why don’t we use them as our internet identities? A name like `alice.poweur.net` is readable, easy to share and built on infrastructure the internet already understands. You can get a hosted name or use a domain you own, backed by cryptographic keys you control. With your own domain, you also control the name itself.

Connect that name to a small, standardized relay service, and it becomes an address where people can reach you, send messages and share files or other data. The relay handles delivery and storage; your devices hold your identity keys. People using different relays can still communicate, much as people using different email providers do. Your name, inbox and data become foundations that different applications can use, rather than things you rebuild every time you join another service.

Once those foundations are in place, there is room for a different kind of app ecosystem. A notes app, a project planner or a shared household budget could keep its data as well-organized files in your own storage, with access for the people you choose. Many familiar apps could work this way without a central backend of their own, using the relay for storage and communication. Switching tools could mean opening the same data in a better interface, without moving everyone you collaborate with to another platform.

Of course, some services need a wider view. Search engines, directories and marketplaces do useful work by indexing the internet and helping us discover people, products and information. I expect we’ll keep needing them. But once you find the person or business you need, much of what follows could happen privately between two Poweur IDs or within a small group. Conversations, orders, agreements and the exchange of files don’t all need to stay inside the service that introduced you. That is where I see the most interesting possibilities for collaboration between people and their agents.

This shared data also makes the handoff to AI agents much simpler. Today, an app may have a great web, mobile or desktop interface, yet be difficult for an agent to use unless its developers provide an API. With the Poweur CLI, an agent can work directly with accessible files in formats it understands. It could update a plan, organize records or prepare a draft, and you would see the result in the familiar app that reads those same files. People and agents can take turns working on the same material.

Poweur ID is live in alpha, still pre-1.0. You can already [claim an ID for free at poweur.net](https://poweur.net/app/) or self-host a relay on your own domain. The CLI, web app and TypeScript SDK are available. Mobile apps can be built from source, and I’m working toward releases on the App Store and Google Play.

My next goal is to bring Poweur ID to as many existing open-source applications as possible, so you can choose an open internet identity instead of a phone number or another platform account. I want it to fit into tools you already like, and let your data and relationships stay with you as those tools change.
