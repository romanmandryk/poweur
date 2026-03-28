# eurything

A pnpm monorepo containing the mobile app and API backend.

## Structure

```
eurything/
├── apps/
│   ├── mobile/   # React Native app (Expo managed workflow)
│   └── api/      # Go HTTP API
├── package.json
├── pnpm-workspace.yaml
└── .gitignore
```

## Prerequisites

- [Node.js](https://nodejs.org/) >= 18
- [pnpm](https://pnpm.io/) >= 9
- [Go](https://go.dev/) >= 1.21
- [Expo CLI](https://docs.expo.dev/more/expo-cli/)

## Getting started

Install JS dependencies from the repo root:

```bash
pnpm install
```

### Mobile (`apps/mobile`)

```bash
pnpm mobile          # start Expo dev server
pnpm mobile:ios      # run on iOS simulator
pnpm mobile:android  # run on Android emulator
```

Or run directly:

```bash
cd apps/mobile
pnpm start
```

### API (`apps/api`)

```bash
cd apps/api
go run main.go       # starts the HTTP server on :8080
```
