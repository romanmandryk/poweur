---
id: rate-limiting
sidebar_position: 6
title: Rate Limiting
---

# Rate Limiting

The relay enforces per-sender rate limits to defend against spam without imposing undue burden on legitimate users. Rate limiting runs **before** any expensive operation — signature verification, DNS lookups, or storage — keeping the rejection path as cheap as possible.

## Default Limits

| Window | Limit | Notes |
|--------|-------|-------|
| Per minute | 20 messages | Burst allowance |
| Per hour | 200 messages | Sustained rate |
| Per day | 1,000 messages | Daily ceiling |

These defaults are calibrated to slightly above average human messaging activity. A normal conversational user will never approach these limits; an automated spammer will be blocked after the first burst.

All three limits are checked on every incoming message. A message is rejected if **any** limit is exceeded.

## Rejection Response

When any rate limit is exceeded, the relay returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{
  "error": "rate_limit_exceeded",
  "window": "minute",
  "limit": 20,
  "reset_at": "2026-03-28T12:01:00Z"
}
```

The `window` field identifies which limit was exceeded (`minute`, `hour`, or `day`). The `reset_at` field is the UTC time at which the sender's counter for that window will reset.

## Rate Limit Key

Rate limits are keyed by **sender identity** — the `sender` field in the submitted message. The relay reads the sender identity before performing signature verification, so the key is based on the claimed identity.

This means an attacker could theoretically claim a victim's identity in the `sender` field to exhaust that identity's rate limit allowance. In practice this is mitigated because:

1. The relay verifies the signature regardless — a message with a forged sender will be rejected with `401` after the rate limit check passes.
2. Rate limit checks happen before signature verification, so the rate limit overhead for a forged sender is minimal.

A stricter implementation would key rate limits by client IP as a fallback for unauthenticated or pre-signature-check throttling.

## Algorithm: Token Bucket

The relay uses a **token bucket** algorithm with a sliding window fallback for the per-hour and per-day limits.

### Token bucket (per-minute)

Each sender has a bucket with a capacity of 20 tokens. The bucket refills at a rate of 20 tokens per 60 seconds (one token every 3 seconds). Each message consumes one token. If the bucket is empty, the message is rejected with `429`.

This allows short bursts (sending 20 messages rapidly) followed by a refill period, which is natural for human messaging behaviour (e.g. a back-and-forth conversation).

### Sliding window (per-hour and per-day)

The hourly and daily limits use a sliding window counter: the relay counts the number of messages from a given sender within the last 3,600 seconds (hour) and 86,400 seconds (day). If either count exceeds its limit, the message is rejected.

### In-memory store

Rate limit state is held in-memory (a map keyed by sender identity). This requires no external dependencies and is straightforward to implement. The tradeoff is that counters are lost on relay restart — which is acceptable for the MVP. Restarting the relay briefly resets all rate limit counters.

For production deployments with multiple relay instances or high message volume, the in-memory store can be replaced with a distributed store such as Redis without changing the rate limiting logic.

## Configuration

All rate limit defaults are configurable via environment variables or the relay config file. The environment variable names are:

| Setting | Env var | Default |
|---------|---------|---------|
| Per-minute limit | `EURYTHING_RATE_LIMIT_MINUTE` | `20` |
| Per-hour limit | `EURYTHING_RATE_LIMIT_HOUR` | `200` |
| Per-day limit | `EURYTHING_RATE_LIMIT_DAY` | `1000` |

Setting any limit to `0` disables that window's check. Setting a limit to `-1` blocks all messages from all senders (useful for maintenance mode).

Example `.env` to raise the hourly limit for a high-volume deployment:

```bash
EURYTHING_RATE_LIMIT_MINUTE=50
EURYTHING_RATE_LIMIT_HOUR=500
EURYTHING_RATE_LIMIT_DAY=5000
```

## Related

- [Relay Overview](/relay/overview)
- [Relay Configuration](/relay/configuration)
- [API Reference](/relay/api-reference)
