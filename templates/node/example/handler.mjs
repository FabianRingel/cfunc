#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Example cfunc handler in Node. Resolves the cfunc SDK via NODE_PATH —
// register with: env=["NODE_PATH=/path/to/sdks/node"]
import { start, Response } from 'cfunc'

await start(async (event, ctx) => {
  return new Response({
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    body: {
      hello: 'world',
      method: event.method,
      path: event.path,
      lang: 'node',
      node_version: process.version,
    },
  })
})
