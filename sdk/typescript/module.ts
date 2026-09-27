// A minimal SDK for modules written in TypeScript, which nodes run in a
// process of their own. It speaks the node's protocol, JSON-RPC 2.0 over
// stdin and stdout (spec/modules.md, "Process modules"), so modules only see
// their inputs, results and the calls they make. Runs on Node.js 23.6 or
// later, which runs TypeScript as is.

import { createInterface } from 'node:readline'

// Env is what a module gets from the node it runs in.
export interface Env {
  nodeId: string
  nodeName: string
  // call calls a capability ("<module>.<capability>") of the node's own
  // modules, and resolves to its result.
  call(ref: string, input?: object): Promise<any>
  // callNode calls a capability of the node with the given ID. The node
  // finds it, and reaches it directly or through a relay.
  callNode(id: string, ref: string, input?: object): Promise<any>
  // notifyNode calls a capability of the node with the given ID like
  // callNode, but doesn't wait for the call to end, nor learn how it did:
  // for news that may as well get lost, such as someone typing.
  notifyNode(id: string, ref: string, input?: object): void
  // emit tells the node's subscribers, such as its users' apps, of the
  // module's event called name, which its manifest declares.
  emit(name: string, body?: object): Promise<void>
  // sign signs data with the node's key, for a purpose starting with the
  // module's name.
  sign(purpose: string, data: Uint8Array): Promise<Uint8Array>
}

// Call is the call being handled.
export interface Call {
  // caller is the ID of the node that made the call.
  caller: string
  // signal aborts when the caller cancels, or gives up.
  signal: AbortSignal
}

export type Handler = (input: any, call: Call) => object | Promise<object>

export interface Module {
  // manifest is the module's module.yaml, as an object.
  manifest: { name: string; [key: string]: unknown }
  // handlers handle the module's capabilities, by name.
  handlers: Record<string, Handler>
  // inspect reports the module's state, for debugging.
  inspect?: () => object
}

// ModuleError fails a call with a code, e.g. "not_found".
export class ModuleError extends Error {
  code: string
  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}

// What a call carries besides its input, in params._meta.
interface Meta {
  timeout?: number
  from?: string
  to?: string
}

// rpcCodes are the JSON-RPC codes of the error codes that have one.
const rpcCodes: Record<string, number> = { invalid_argument: -32602, unimplemented: -32601 }

// serve runs the module start returns, for the node that started this
// process, until the node closes stdin.
export function serve(start: (config: any, env: Env) => Module | Promise<Module>): void {
  if (process.env.DECENTRALIZED_PROTOCOL !== '1') {
    console.error('module: DECENTRALIZED_PROTOCOL must be 1: process modules are started by a node')
    process.exit(1)
  }
  // stdout carries messages only.
  console.log = console.info = console.debug = console.error

  let module: Module | undefined
  let next = 0
  const pending = new Map<number, { resolve: (result: any) => void; reject: (err: Error) => void }>()
  const running = new Map<number | string, AbortController>()
  const send = (m: object) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...m }) + '\n')

  const call = (method: string, input: object = {}, meta: Meta = {}): Promise<any> =>
    new Promise((resolve, reject) => {
      const id = ++next
      pending.set(id, { resolve, reject })
      send({ id, method, params: { ...input, _meta: meta } })
    })

  const env = (node: { id: string; name: string }): Env => ({
    nodeId: node.id,
    nodeName: node.name,
    call: (ref, input) => call(ref, input),
    callNode: (id, ref, input) => call(ref, input, { to: id }),
    notifyNode: (id, ref, input = {}) => send({ method: ref, params: { ...input, _meta: { to: id } } }),
    emit: async (name, body = {}) => {
      await call('node.emit', { name, body })
    },
    sign: async (purpose, data) => {
      const out = await call('node.sign', { purpose, data: Buffer.from(data).toString('base64') })
      return Buffer.from(out.signature, 'base64')
    },
  })

  // handle handles a call from the node, and resolves to its result.
  const handle = async (method: string, input: any, meta: Meta, signal: AbortSignal): Promise<object> => {
    if (method === 'module.start') {
      if (module) throw new ModuleError('invalid_argument', 'already started')
      module = await start(input.config ?? {}, env(input.node))
      return { manifest: module.manifest }
    }
    if (method === 'module.inspect') return module?.inspect?.() ?? {}
    const prefix = module ? module.manifest.name + '.' : '\0'
    const handler = method.startsWith(prefix) ? module?.handlers[method.slice(prefix.length)] : undefined
    if (!handler) throw new ModuleError('unimplemented', `no capability ${method}`)
    return handler(input, { caller: meta.from ?? '', signal })
  }

  const receive = (m: any) => {
    if (m.method && m.id === undefined) {
      if (m.method === '$/cancelRequest') running.get(m.params?.id)?.abort()
      return // the node doesn't send others
    }
    if (m.method) {
      const { _meta: meta = {}, ...input } = m.params ?? {}
      const abort = new AbortController()
      running.set(m.id, abort)
      const timer = meta.timeout ? setTimeout(() => abort.abort(), meta.timeout) : undefined
      handle(m.method, input, meta, abort.signal)
        .then(result => send({ id: m.id, result }))
        .catch(err => {
          const code = err instanceof ModuleError ? err.code : 'unknown'
          send({ id: m.id, error: { code: rpcCodes[code] ?? -32000, message: String(err?.message ?? err), data: { code } } })
        })
        .finally(() => {
          clearTimeout(timer)
          running.delete(m.id)
        })
      return
    }
    const call = pending.get(m.id)
    pending.delete(m.id)
    if (m.error) call?.reject(new ModuleError(m.error.data?.code ?? 'unknown', m.error.message))
    else call?.resolve(m.result ?? {})
  }

  createInterface({ input: process.stdin })
    .on('line', line => line.trim() && receive(JSON.parse(line)))
    .on('close', () => process.exit(0))
}
