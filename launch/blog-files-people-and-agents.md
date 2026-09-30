---
title: "The shared folder deserves a second chance"
description: "Readable files, clear permissions and good tools could give people and AI agents a common place to work."
author: Roman Mandryk
date: 2026-10-27
draft: true
---

# The shared folder deserves a second chance

A folder containing `brief.md`, `prices.csv` and `proposal.md` does not look like the future of work.

It looks like something you forgot to tidy on Friday.

But suppose a colleague can read the brief, an agent can use the prices to prepare a draft, and you can revise the proposal in an editor you like. Suppose another tool can turn the result into a PDF without requiring you to move the whole project into its account system.

There is a lot of future in that rather ordinary folder.

The agentic era is an opportunity to revisit where our work lives. If we put agents on top of the same disconnected applications, we can make navigating those applications faster. We can also ask whether some of the navigation should be necessary at all.

## Give the work a home

Consider a small design studio preparing a proposal.

The brief arrives in chat. Pricing lives in a spreadsheet. The draft lives in a document service. Someone copies feedback between them. An assistant is added, and now it needs access to all three, plus enough context to work out which content is current.

Each application may be excellent. The awkwardness is in the handoffs.

A different arrangement starts with a project home. The brief, pricing and proposal belong to the project. Applications provide different ways to work on those materials. The assistant becomes another participant with a specific assignment.

In that arrangement, the brief could be Markdown, the price list CSV, and the machine-readable job status JSON. A person could use a polished visual editor without ever seeing the filenames. An agent could read the same underlying information directly.

Changing the editor would not require moving the project. Adding an assistant would not require making the assistant's vendor the new owner of the workspace.

That is the direction I want shared data in Poweur to support. The complete workflow described here is a design goal, rather than a claim that an integrated proposal assistant already ships.

## Why text helps

Text makes several useful things cheap.

You can inspect it without the original application. You can search a collection of files with many different tools. You can compare two versions and see what changed. You can copy a project somewhere else without first asking a vendor to invent an export format.

Agents benefit from the same properties. A clearly written brief is available to a person, a script and a language model. A small structured record can carry an exact price or a status without asking software to infer it from a screenshot.

Text is not the best storage format for everything. Photographs, video, complex design documents and large datasets have their own needs. Even a text file can be effectively proprietary if nobody understands its structure.

The useful property is an open, documented representation that multiple tools can work with. Text is often a very good way to get there.

And it needs conventions. Two applications seeing the same JSON is only the beginning. They must agree on what the fields mean, how versions work and how to handle information they don't recognise. Otherwise, you have moved the integration problem into a file and given it a reassuring extension.

## Sharing a workspace doesn't mean sharing every permission

In the proposal example, the assistant needs to read the brief and prices and write a draft. It doesn't need access to payroll. It doesn't need permission to send the proposal to the customer or approve a discount.

Those are separate decisions, even if all the material belongs to the same business.

An agent should have an identifiable role, access limited to the job and a clear way to end that access. Its output should leave enough history for a person to review the work. Reading a document must not make instructions inside that document authoritative: a customer's attachment cannot grant the assistant new permissions.

The collaboration system also has to distinguish a suggestion from an approved change. A person might review a proposed edit before it replaces the current price list. Sending a finished proposal can require a separate approval from drafting it.

Revoking access stops future authorised access. It cannot make an agent or person forget information already read. That makes choosing what to share important from the start.

These are requirements for a useful common workspace. They are also why “just give the agent a folder” is the beginning of a design, rather than the whole design.

## Keep the interface pleasant and the data accessible

None of this requires everybody to learn Git or edit JSON at breakfast.

A task application can still show a board. A document editor can still offer comments, suggestions and formatting. An assistant can still have a conversational interface. Those tools can all work with data whose meaning and location remain understandable outside the tool.

Reliable collaboration also needs coordination. If two people edit a file at once, the system must merge their work safely or make the conflict visible. Keeping old versions, validating changes and reporting failures are part of the product. A folder full of silently overwritten files would be a very traditional future, and not one worth recreating.

What excites me is the possibility of improving tools without repeatedly rebuilding the home of the work itself.

A small developer could make the best proposal editor for architects. Another could build a useful research assistant. A studio could use both on the same project, with access it understands and files it can keep.

The next big improvement in working with agents may be a very modest thing: giving everyone a common place to put the work, and making it easy to see what happened there.
