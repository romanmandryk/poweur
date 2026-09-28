---
id: connected-apps
sidebar_position: 3
title: Connected apps
---

# Connected apps

The v1 path-scoped grant exchange (`/auth/grant`) and app bearer tokens were
removed with WebDAV. Sign-in approvals still authenticate a user, but they do
not grant access to a drive.

Storage v2 will use scoped drive handles and node shares (EPIC-020 E20-T7/T8).
The baseline must restore connected-app records and the encrypted consent log at
`.poweur/private/logs/auth.log`. Until then, the temporary system-file adapter
can store existing connection metadata but cannot provide v2 resource grants or
durable private consent history. See [Storage v2](../files/storage-v2.md).
