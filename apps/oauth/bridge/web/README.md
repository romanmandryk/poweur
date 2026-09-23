# Bridge pages (built)

`dist/` is the build of [`apps/oauth/ui`](../../ui) (React), embedded into the
bridge binary by `web.go`. It is not committed:

```bash
pnpm --filter @poweur/oauth-ui build    # or: watch
```

Without it the bridge still runs and serves every page's data, behind a plain
"pages not built" notice — enough for the Go tests, not for people. The image
(`apps/oauth/Dockerfile`) and CI build it.
