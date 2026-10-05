---
title: "Introducing Poweur ID: your name, your inbox, your files"
description: "An open identity for signing in, messaging and sharing data across providers, backed by keys you control."
author: Roman Mandryk
date: 2026-10-13
draft: true
---

# Introducing Poweur ID: your name, your inbox, your files

Every time we start using a new app, we repeat a familiar process: create an account, find the people we know, upload our files and set up permissions. Over time, our online lives become scattered across services that each hold a different part of who we are and what we do.

I’m building Poweur because I think these everyday foundations should work across applications and providers. We should be able to try a better tool without asking everyone we work with to move there too.

A **Poweur ID** is a readable name, such as `alice.poweur.net` or a name on your own domain, backed by cryptographic keys you control. It gives you an identity for signing in, an inbox for messages and a home for files you want to keep or share.

These three things belong together. When someone sends you a message, you want to know who sent it. When you share a document, you want to decide who can read or change it. When you open an app, you want it to recognise you and have access only to what it needs. Poweur brings those decisions around one identity.

Messages are signed and end-to-end encrypted, and they can travel between people using different Poweur relays. Files can be shared with other identities. Applications configured to accept Poweur can let you sign in with your ID. The aim is to make these familiar actions work without requiring everyone to have an account with the same company.

Email showed how valuable that can be: people using different providers can still reach one another. Poweur builds on that principle of interoperability, bringing cryptographic identity, messaging and shared data together through open protocols.

You can use a hosted service or run your own relay. A hosted name still depends on the operator of its domain; using your own domain gives you more control over your address. Hosting remains a useful service, but open-source software and a shared protocol give you more choice about who provides it.

This becomes especially interesting as agents take on more work alongside us. Imagine sharing a project brief and a price list with an assistant, then reviewing its draft in the editor you already use. Readable files and open formats give people and software a common place to work. The longer-term goal is for agents to participate through their own identities, with access limited to the job and a clear way to withdraw it.

Poweur is still pre-1.0. The core identity, messaging, sharing and sign-in features exist; the broader agent experience and app ecosystem are still ahead. There is plenty to build, test and improve, and the code is open for anyone who wants to follow or contribute.

The vision is an internet where more of our life and work can happen directly between people, communities and businesses, through services we choose. Directories and search services can help us find each other without becoming the owners of every relationship that follows.

I want our names, relationships and work to remain ours as the tools around them change. Poweur is my attempt to make that practical.

[Explore Poweur and follow development →](https://github.com/romanmandryk/poweur)
