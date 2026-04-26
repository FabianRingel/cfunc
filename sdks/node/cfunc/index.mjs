// cfunc Node SDK — write an async handler, call start(handler).
//
// Mirrors the Go and Python SDKs: connects to CFUNC_SOCKET, reads
// length-prefixed JSON frames, dispatches each invoke to the handler,
// writes back result/error frames. Sequential per connection.

import net from 'node:net'
import process from 'node:process'

const HEADER_SIZE = 4
const MAX_FRAME = 16 * 1024 * 1024 // 16 MiB

/**
 * Construct a Response. Body is JSON-serialized as-is; pass a string,
 * object, or array. Headers default to {}.
 */
export class Response {
  constructor({ status = 200, headers = {}, body = null } = {}) {
    this.status = status
    this.headers = headers
    this.body = body
  }
}

class FrameReader {
  constructor() {
    this._chunks = []
    this._size = 0
  }
  push(chunk) {
    this._chunks.push(chunk)
    this._size += chunk.length
  }
  /** Returns the next decoded frame, or null if more data is needed. */
  next() {
    if (this._size < HEADER_SIZE) return null
    this._coalesce()
    const buf = this._chunks[0]
    const n = buf.readUInt32BE(0)
    if (n === 0) throw new Error('cfunc: zero-length frame')
    if (n > MAX_FRAME) throw new Error(`cfunc: frame too large: ${n}`)
    if (buf.length < HEADER_SIZE + n) return null
    const payload = buf.subarray(HEADER_SIZE, HEADER_SIZE + n)
    this._chunks[0] = buf.subarray(HEADER_SIZE + n)
    this._size = this._chunks[0].length
    return JSON.parse(payload.toString('utf8'))
  }
  _coalesce() {
    if (this._chunks.length <= 1) return
    const merged = Buffer.concat(this._chunks, this._size)
    this._chunks = [merged]
  }
}

function writeFrame(sock, frame) {
  const payload = Buffer.from(JSON.stringify(frame), 'utf8')
  if (payload.length > MAX_FRAME) {
    throw new Error(`cfunc: frame too large: ${payload.length}`)
  }
  const hdr = Buffer.alloc(4)
  hdr.writeUInt32BE(payload.length, 0)
  sock.write(hdr)
  sock.write(payload)
}

/**
 * start(handler) connects to CFUNC_SOCKET, serves frames, resolves on EOF.
 *
 * handler signature: (event, ctx) => Response | Promise<Response>
 */
export async function start(handler) {
  const sockPath = process.env.CFUNC_SOCKET
  if (!sockPath) throw new Error('cfunc: CFUNC_SOCKET not set')

  return new Promise((resolve, reject) => {
    const sock = net.createConnection(sockPath)
    const reader = new FrameReader()
    let chain = Promise.resolve()
    let done = false

    const finish = (err) => {
      if (done) return
      done = true
      err ? reject(err) : resolve()
    }
    sock.on('error', finish)
    sock.on('end', () => finish())
    sock.on('close', () => finish())

    sock.on('data', (chunk) => {
      reader.push(chunk)
      // Process all currently-buffered frames in order. Chain via the
      // existing promise so handlers see strict per-connection order.
      chain = chain.then(async () => {
        while (true) {
          let frame
          try {
            frame = reader.next()
          } catch (e) {
            sock.destroy(e)
            return
          }
          if (!frame) break
          await dispatch(sock, frame, handler)
          if (frame.type === 'shutdown') {
            sock.end()
            return
          }
        }
      }).catch((e) => finish(e))
    })
  })
}

async function dispatch(sock, frame, handler) {
  const id = frame.id || ''
  switch (frame.type) {
    case 'invoke': {
      try {
        const event = frame.event || {}
        const ctx = frame.ctx || {}
        const result = await handler(event, ctx)
        const r = result instanceof Response ? result : new Response(result || {})
        writeFrame(sock, {
          type: 'result',
          id,
          result: { status: r.status, headers: r.headers || {}, body: r.body },
        })
      } catch (e) {
        writeFrame(sock, {
          type: 'error',
          id,
          error: {
            type: (e && e.name) || 'Error',
            message: String((e && e.message) || e),
            stack: (e && e.stack) || '',
          },
        })
      }
      return
    }
    case 'init':
      writeFrame(sock, { type: 'init_ok', id })
      return
    case 'shutdown':
      writeFrame(sock, { type: 'shutdown_ok', id })
      return
    default:
      writeFrame(sock, {
        type: 'error', id,
        error: { type: 'ProtocolError', message: `unknown frame type: ${frame.type}` },
      })
  }
}

export default { start, Response }
