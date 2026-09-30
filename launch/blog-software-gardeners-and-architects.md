---
title: "Software gardeners and architects"
description: "Explore, simplify, ship, and explore again. Good software grows through repeated changes between gardening and architecture."
author: Roman Mandryk
date: 2026-10-20
draft: true
---

# Software gardeners and architects

George R. R. Martin describes two approaches to writing: architects and gardeners. Architects work from a plan. Gardeners plant an idea, follow its growth and discover parts of the story along the way. Martin places himself firmly among the gardeners.

You can see the attraction in *A Song of Ice and Fire*, the novels behind *Game of Thrones*. Characters have room to surprise you. A seemingly minor branch can become something worth following for hundreds of pages. Eventually, remembering everyone's relatives begins to feel like maintaining a production dependency graph.

You can also see the difficulty. In his 2022 essay [“A Winter Garden”](https://georgerrmartin.com/notablog/2022/07/08/a-winter-garden/), Martin describes rewriting, restructuring and a growing cast whose interwoven stories help explain the long work on *The Winds of Winter*. Bringing so much growth to a satisfying ending is an enormous coordination problem. Calling it impossible would be unfair; calling it familiar to a software developer seems reasonable.

Software has gardeners and architects too. I think the useful question is when to change roles.

## First, find out what can grow

At the beginning of a project, you don't know enough to design its final architecture.

You may know the problem you want to solve. You probably have strong opinions about the solution. What you don't yet have is evidence about which parts belong together, which ideas deserve their own concepts, or which clever abstractions will turn out to be unnecessary.

This is a good time to garden.

Try the feature. Build a rough version. Follow an unexpected use case far enough to understand it. Let two implementations remain slightly different until you know whether their differences matter.

Imagine building a tool for a small club. You start with announcements. Someone needs event registration. Another person wants a shared checklist. You experiment with all three and discover that they share membership, permissions and notifications, but have quite different rules about who can change what.

That discovery is more valuable than a beautifully general “club object framework” designed before anyone used the tool.

Gardening still needs boundaries. Keep experiments small, protect user data and test the parts whose failure would hurt people. Security and recovery cannot wait for the tidy-up phase. The freedom is to change your product assumptions, not to abandon engineering care.

## Then make something you can finish

Eventually, another feature teaches you less than stepping back would.

You notice the same permission check in four places. A person has become a “member”, a “participant” and a “subscriber”, with almost identical behaviour. Every new screen needs a slightly different notification system.

Now the architect has useful evidence.

Find the common building blocks. Give them clear responsibilities. Keep differences where the behaviour really differs. Reduce the number of concepts someone must hold in their head to understand the product.

Then choose a small, complete path through it and ship the MVP. For the club, that might be creating an event, inviting members and seeing who will attend. The rest can wait. A first release needs to complete a useful job; it does not need to contain every interesting thing the prototype revealed.

A minimal product can have a substantial foundation. The discipline is making that foundation serve the first real use case, rather than constructing a grand platform for hypothetical customers.

## Customers reopen the garden

Once people use the software, they bring lives that are less tidy than your examples.

One club needs guest invitations. Another runs recurring events. Someone wants several organisers. Someone else needs an attendance export for the council. Your elegant MVP has become the start of a much larger conversation.

Garden again. Collect those requests, investigate the underlying problems and try different answers. A customer asking for a new dashboard may really need one notification at the right moment. You learn that by exploring with them.

The danger is treating every request as a permanent addition to the core. Eventually the product becomes a record of every sales conversation it has ever had. The settings page develops its own weather system.

At that point, return to the product's core promise. What is the distinctive job people choose it for? Which requests strengthen that promise? Which are specialised variations? Which belong in a different product entirely?

This round of architecture often produces a stronger core with modules around it. Membership and permissions stay dependable. Event registration, ticketing and attendance reporting can develop at different speeds without each changing what it means to be a member.

A module boundary earns its place when it contains change. Putting a feature in a different directory is only the administrative part.

## Plugins start another growing season

Once others can extend the product, the exploration becomes much bigger than your own roadmap.

A third-party developer connects accounting software. A community adds a workflow you have never encountered. Someone builds an accessibility tool that changes how you think about the interface. This is one of the best reasons to support plugins: other people know things you don't.

It also puts the architecture under pressure.

Extensions may need the same missing capability. Several plugins may depend on an internal detail that was never meant to be public. Your original permissions model may not express the access they actually need.

Those are signals to review the boundaries again. Promote a repeatedly useful capability into a stable interface. Separate concerns that have become tangled. Sometimes the evidence supports a larger vision for the whole product.

That doesn't mean forcing everyone through a rewrite. Once other people depend on your interfaces, architectural work includes compatibility, migrations and time to adapt. A cleaner core that breaks the ecosystem has passed its costs to someone else.

The next architecture should preserve what people value while making the next round of exploration less expensive.

## Know which phase you are in

The cycle I aim for is simple: explore the possibilities, identify the useful structure, ship something coherent, and let real use challenge it.

You can recognise the need to change modes by the kind of uncertainty you're facing. When you don't know which behaviour matters, experiments help. When you know what matters but every change touches everything, architecture helps. When the core serves its purpose and specialised requests keep arriving, extension points may help.

This is how I think about Poweur's scope. Messaging, shared documents, agents and small apps open a very large garden. Identity, messaging and shared data are the common building blocks I want to keep understandable beneath that variety. Every additional use case should teach us whether those boundaries are right.

A product needs enough freedom to discover what it could become, and enough structure to deliver what it has promised. The skill is noticing when to put down the watering can and pick up the plans—and when the plans need another season in the garden.
