# @meta-core/editor

React + Vite metadata browser/editor that ships inside the meta-core
container. Walks the UUID-rooted Redis keyspace, edits leaf values,
imports/exports snapshots, and consults the live schema indexer.

## What it talks to

The editor is a pure SPA — it has no server of its own. It calls the
meta-core HTTP API directly under three prefixes (see `src/api/`):

- `/api/kv/*` — slash-tree KV browser (`info`, `tree`, `search`, `find`,
  `value`, `key/{key...}`). This is the UUID-exposing surface — keys look
  like `file:<uuid>/<property>` and `cid:<algo>:<value>`.
- `/api/schema` — live per-field schema (type hints, value breakdowns),
  produced by meta-core's schema indexer that consumes `meta:events`.
- `/api/snapshot/*` — snapshot export / import / wipe.

In production the editor is served by meta-core's nginx (the Vite build
output is copied into the image at the same path) under
`https://metacore-dev.localhost:8083/editor/`. The Vite `base` is set to
`/editor/` accordingly.

## Project layout

```
packages/meta-core/editor/
├── index.html
├── vite.config.ts
├── tsconfig.json
├── package.json                @meta-core/editor
└── src/
    ├── main.tsx                entry
    ├── api/
    │   ├── kvApi.ts            /api/kv client
    │   ├── schemaApi.ts        /api/schema client + cache
    │   └── snapshotApi.ts      /api/snapshot client
    ├── kv/                     KV browser + leaf/branch editors
    ├── components/             shared widgets (language combobox, snapshot panel, ...)
    └── index.css
```

## Local development

Prerequisites: Node 21.6.2+, pnpm pinned in the root `package.json`.

```bash
cd packages/meta-core/editor
pnpm install
pnpm dev          # vite dev server on http://localhost:5173
```

The Vite dev server proxies `/api/*` to `http://localhost:3000` (see
`vite.config.ts`). For local development against a running meta-core
container, change that target to wherever meta-core is reachable on your
host — e.g. `http://localhost:18083` for the dev compose's direct-backend
port.

No root-level `start:editor` or `build:editor` scripts exist; run pnpm
commands from this directory.

## Build

```bash
pnpm build        # tsc -b && vite build → dist/
```

The container build copies `dist/` into the nginx web root so the editor
is served at `/editor/`.

## Tech

- React 18, TypeScript 5
- Vite 5 (`base: '/editor/'`)
- Native CSS — no UI framework
- `iso-639-3` for language code rendering
