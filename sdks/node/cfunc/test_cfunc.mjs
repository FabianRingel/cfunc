// SPDX-License-Identifier: Apache-2.0

// Run with: node --test sdks/node/cfunc/test_cfunc.mjs

import { test } from 'node:test'
import assert from 'node:assert/strict'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'

import { start, Response } from './index.mjs'

function makePair() {
  const dir = mkdtempSync(join(tmpdir(), 'cfunc-test-'))
  const sock = join(dir, 's.sock')
  return { dir, sock }
}

function writeFrame(sock, frame) {
  const payload = Buffer.from(JSON.stringify(frame), 'utf8')
  const hdr = Buffer.alloc(4)
  hdr.writeUInt32BE(payload.length, 0)
  sock.write(Buffer.concat([hdr, payload]))
}

function readFrames(sock, expected, timeoutMs = 2000) {
  return new Promise((resolve, reject) => {
    const out = []
    let buf = Buffer.alloc(0)
    const t = setTimeout(() => reject(new Error(`timeout waiting for ${expected} frames`)), timeoutMs)
    sock.on('data', (chunk) => {
      buf = Buffer.concat([buf, chunk])
      while (buf.length >= 4) {
        const n = buf.readUInt32BE(0)
        if (buf.length < 4 + n) break
        out.push(JSON.parse(buf.subarray(4, 4 + n).toString('utf8')))
        buf = buf.subarray(4 + n)
        if (out.length >= expected) {
          clearTimeout(t)
          resolve(out)
          return
        }
      }
    })
  })
}

async function withSDK(handler, fn) {
  const { dir, sock: sockPath } = makePair()
  process.env.CFUNC_SOCKET = sockPath
  try {
    const server = net.createServer()
    const conn = new Promise((resolve) => server.once('connection', resolve))
    await new Promise((resolve, reject) => server.listen(sockPath, resolve).once('error', reject))
    const sdkDone = start(handler)
    const peer = await conn
    try {
      await fn(peer)
    } finally {
      peer.end()
      server.close()
      await sdkDone.catch(() => {})
    }
  } finally {
    rmSync(dir, { recursive: true, force: true })
    delete process.env.CFUNC_SOCKET
  }
}

test('invoke round-trip', async () => {
  await withSDK(
    async (event) => new Response({ status: 200, body: { echo: event.path } }),
    async (peer) => {
      writeFrame(peer, { type: 'invoke', id: 'r1', event: { method: 'GET', path: '/x' } })
      const [frame] = await readFrames(peer, 1)
      assert.equal(frame.type, 'result')
      assert.equal(frame.id, 'r1')
      assert.equal(frame.result.status, 200)
      assert.equal(frame.result.body.echo, '/x')
    },
  )
})

test('handler exception becomes error frame', async () => {
  await withSDK(
    async () => { throw new Error('kapow') },
    async (peer) => {
      writeFrame(peer, { type: 'invoke', id: 'r2' })
      const [frame] = await readFrames(peer, 1)
      assert.equal(frame.type, 'error')
      assert.equal(frame.error.message, 'kapow')
      assert.ok(frame.error.stack.length > 0)
    },
  )
})

test('shutdown is acknowledged', async () => {
  await withSDK(
    async () => new Response({ status: 200 }),
    async (peer) => {
      writeFrame(peer, { type: 'shutdown', id: 'sd' })
      const [frame] = await readFrames(peer, 1)
      assert.equal(frame.type, 'shutdown_ok')
    },
  )
})

test('plain object return is wrapped in Response', async () => {
  await withSDK(
    async () => ({ status: 201, body: 'ok' }),
    async (peer) => {
      writeFrame(peer, { type: 'invoke', id: 'r3' })
      const [frame] = await readFrames(peer, 1)
      assert.equal(frame.result.status, 201)
      assert.equal(frame.result.body, 'ok')
    },
  )
})
