// The chat web app, on a node's local API: the chat module's capabilities,
// and its events over /v1/events. You are your root identity, a did:key;
// this browser holds a device key the root authorized (for now by pasting
// the authorization from the CLI, later through the vault), signs in to the
// node with it, and signs every event with it. The node verifies, stores
// and pushes them to the other members' nodes. See spec/identity.md and
// docs/design/identity-auth.md.

interface User {
  id: string // the person's DID
  name: string
  node: string // where they registered
}

interface Membership {
  user: string
  node: string
}

interface Channel {
  id: string
  name?: string
  kind: 'channel' | 'dm'
  heads: string[]
  members?: Membership[]
}

interface Event {
  id: string
  channel: string
  author: string
  kind: 'create' | 'invite' | 'join' | 'message' | 'edit' | 'reaction' | 'receipt'
  body?: Record<string, any>
  parents?: string[]
  time: string
}

// Signed is data someone signed, as spec/identity.md carries it.
interface Signed {
  data: string // base64
  signer: string
  sig: string // base64
  delegation?: Signed
}

// A message as shown: its latest text, and its reactions.
interface Message {
  event: Event
  text: string
  edited: boolean
  reactions: Map<string, Set<string>> // emoji -> who
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T
const who = $<HTMLDialogElement>('who')
const pick = $<HTMLDialogElement>('pick')
const authorizeDialog = $<HTMLDialogElement>('authorize')
const statusEl = $('status')
const encoder = new TextEncoder()

// --- Identity -------------------------------------------------------------

const alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'

// base58 encodes bytes in base58btc, as did:key needs.
function base58(bytes: Uint8Array): string {
  let n = 0n
  for (const b of bytes) n = (n << 8n) | BigInt(b)
  let out = ''
  while (n > 0n) {
    out = alphabet[Number(n % 58n)] + out
    n /= 58n
  }
  for (const b of bytes) {
    if (b !== 0) break
    out = '1' + out
  }
  return out
}

const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const unbase64 = (s: string) => Uint8Array.from(atob(s), c => c.charCodeAt(0))

// idb opens the browser's small database for the key and its authorization.
function idb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open('chat', 1)
    req.onupgradeneeded = () => req.result.createObjectStore('keys')
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function idbGet<T>(key: string): Promise<T | undefined> {
  const db = await idb()
  return new Promise((resolve, reject) => {
    const req = db.transaction('keys').objectStore('keys').get(key)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function idbPut(key: string, value: unknown): Promise<void> {
  const db = await idb()
  return new Promise((resolve, reject) => {
    const tx = db.transaction('keys', 'readwrite')
    tx.objectStore('keys').put(value, key)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

// Device is this browser's key, and the root's authorization of it.
class Device {
  constructor(
    readonly did: string,
    private keys: CryptoKeyPair,
    public authorization: Signed | undefined,
  ) {}

  // load returns the device kept in this browser, making a key the first
  // time.
  static async load(): Promise<Device> {
    let keys = await idbGet<CryptoKeyPair>('device')
    if (!keys) {
      keys = (await crypto.subtle.generateKey({ name: 'Ed25519' }, false, ['sign', 'verify'])) as CryptoKeyPair
      await idbPut('device', keys)
    }
    const raw = new Uint8Array(await crypto.subtle.exportKey('raw', keys.publicKey))
    const multicodec = new Uint8Array([0xed, 0x01, ...raw]) // ed25519-pub
    return new Device('did:key:z' + base58(multicodec), keys, await idbGet<Signed>('authorization'))
  }

  // person is the root the device acts for.
  get person(): string {
    return this.authorization!.signer
  }

  // authorized reports whether the device holds an authorization for itself
  // that hasn't expired.
  authorized(): boolean {
    const a = this.authorization
    if (!a) return false
    try {
      const grant = JSON.parse(new TextDecoder().decode(unbase64(a.data)))
      return grant.device === this.did && new Date(grant.expires).getTime() > Date.now() + 60_000
    } catch {
      return false
    }
  }

  async authorize(pasted: string): Promise<void> {
    const a: Signed = JSON.parse(pasted)
    if (!a.data || !a.signer || !a.sig) throw new Error('not an authorization')
    this.authorization = a
    if (!this.authorized()) {
      this.authorization = undefined
      throw new Error("this authorization isn't for this device, or has expired")
    }
    await idbPut('authorization', a)
  }

  // sign signs v for purpose, for the root: as nodes do, over
  // "decentralized-signature" NUL purpose NUL data (spec/modules.md,
  // "Signing"), carrying the root's delegation.
  async sign(purpose: string, v: unknown): Promise<Signed> {
    const data = encoder.encode(JSON.stringify(v))
    const message = new Uint8Array([...encoder.encode('decentralized-signature\0'), ...encoder.encode(purpose), 0, ...data])
    const sig = new Uint8Array(await crypto.subtle.sign('Ed25519', this.keys.privateKey, message))
    return { data: base64(data), signer: this.did, sig: base64(sig), delegation: this.authorization }
  }
}

// --- The node --------------------------------------------------------------

// call calls a chat capability, as the person signed in.
async function call<T = any>(name: string, input: object = {}): Promise<T> {
  const res = await fetch(`/v1/capabilities/chat.${name}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  const body = await res.json()
  if (!res.ok) throw new Error(body.message ?? res.statusText)
  return body
}

// login signs the device in to the node: a challenge, signed for the node.
async function login(): Promise<void> {
  const challenge = await (await fetch('/v1/challenge')).json()
  const signed = await device.sign('node.login', { challenge: challenge.challenge, node: challenge.node })
  const res = await fetch('/v1/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ signed }) })
  if (!res.ok) throw new Error(`sign-in failed: ${(await res.json()).message ?? res.statusText}`)
}

let device: Device
let me: User
const users = new Map<string, User>()
let channels: Channel[] = []
let current: Channel | undefined
const typing = new Map<string, number>() // user -> when they last typed, in the current channel
const online = new Set<string>() // who's online, as the node knows

const nameOf = (id: string) => users.get(id)?.name ?? id.slice(-8)
const address = (u: User) => `${u.id}@${u.node}`

// submit signs an event and hands it to the node.
async function submit(event: Omit<Event, 'id'> | Record<string, unknown>): Promise<Event> {
  return call<Event>('submit', { signed: await device.sign('chat.event', event) })
}

// post signs and submits an event on the current channel, naming its heads.
async function post(kind: Event['kind'], body: object): Promise<Event | undefined> {
  if (!current) return undefined
  return submit({ channel: current.id, author: me.id, kind, body, parents: current.heads, time: new Date().toISOString() }).catch(fail)
}

// getAuthorized has the root authorize this device, if it isn't yet.
async function getAuthorized(): Promise<void> {
  while (!device.authorized()) {
    $('device-did').textContent = device.did
    $('copy-device').onclick = () => navigator.clipboard.writeText(device.did).catch(fail)
    const pasted = $<HTMLTextAreaElement>('authorization')
    pasted.value = ''
    authorizeDialog.showModal()
    await new Promise<void>(resolve => (authorizeDialog.onclose = () => resolve()))
    try {
      await device.authorize(pasted.value.trim())
    } catch (err) {
      fail(err)
    }
  }
}

async function ask(name: string): Promise<User> {
  return new Promise(resolve => {
    const input = $<HTMLInputElement>('name')
    input.value = name
    who.showModal()
    who.onclose = async () => {
      const signed = await device.sign('chat.user', { name: input.value.trim() || 'Anonymous', time: new Date().toISOString() })
      resolve(await call<User>('register', { signed }))
    }
  })
}

async function loadUsers() {
  const { users: list } = await call<{ users: User[] }>('users')
  users.clear()
  for (const u of list) users.set(u.id, u)
}

async function loadChannels() {
  channels = (await call<{ channels: Channel[] }>('channels')).channels
  if (current) current = channels.find(ch => ch.id === current!.id) ?? current
  renderChannels()
}

function renderChannels() {
  for (const [id, kind] of [['channels', 'channel'], ['dms', 'dm']] as const) {
    $(id).replaceChildren(
      ...channels
        .filter(ch => ch.kind === kind)
        .map(ch => {
          const li = document.createElement('li')
          li.textContent = label(ch)
          li.classList.toggle('current', ch.id === current?.id)
          li.onclick = () => open(ch)
          return li
        }),
    )
  }
}

const memberIDs = (ch: Channel) => (ch.members ?? []).map(m => m.user)

// label names a channel: its name, or for a direct message the other side.
function label(ch: Channel): string {
  if (ch.kind === 'dm') return memberIDs(ch).filter(id => id !== me.id).map(nameOf).join(', ') || 'Just you'
  return '#' + (ch.name || ch.id.slice(0, 8))
}

async function open(ch: Channel) {
  current = ch
  typing.clear()
  $('title').textContent = label(ch)
  $('invite').hidden = false
  $('compose').hidden = false
  renderChannels()
  const { online: ids } = await call<{ online: string[] }>('online', { users: memberIDs(ch) })
  for (const id of memberIDs(ch)) online.delete(id)
  for (const id of ids) online.add(id)
  renderMembers()
  await refresh()
}

// renderMembers lists who is in the current channel, and who is online.
function renderMembers() {
  if (!current) return
  $('members').textContent = memberIDs(current)
    .map(id => `${online.has(id) ? '●' : '○'} ${nameOf(id)}`)
    .join('  ')
}

// refresh shows the current channel as the node has it now, and marks it
// read.
async function refresh() {
  if (!current) return
  await loadUsers()
  const { events } = await call<{ events: Event[] }>('history', { channel: current.id, limit: 500 })
  renderMessages(fold(events))
  const last = events.at(-1)
  if (last && last.author !== me.id && last.kind === 'message') post('receipt', { event: last.id })
}

// fold turns a channel's events into the messages to show: edits replace
// text, reactions gather on their message.
function fold(events: Event[]): Message[] {
  const messages = new Map<string, Message>()
  for (const e of events) {
    switch (e.kind) {
      case 'message':
        messages.set(e.id, { event: e, text: e.body?.text ?? '', edited: false, reactions: new Map() })
        break
      case 'edit': {
        const m = messages.get(e.body?.event)
        if (m && e.author === m.event.author) Object.assign(m, { text: e.body?.text ?? m.text, edited: true })
        break
      }
      case 'reaction': {
        const m = messages.get(e.body?.event)
        if (!m) break
        const emoji = e.body?.emoji ?? ''
        if (!m.reactions.has(emoji)) m.reactions.set(emoji, new Set())
        m.reactions.get(emoji)!.add(e.author)
        break
      }
    }
  }
  return [...messages.values()]
}

function renderMessages(messages: Message[]) {
  const ol = $('messages')
  const atBottom = ol.scrollHeight - ol.scrollTop - ol.clientHeight < 40
  ol.replaceChildren(
    ...messages.map(m => {
      const li = document.createElement('li')
      li.classList.toggle('mine', m.event.author === me.id)
      const meta = document.createElement('div')
      meta.className = 'meta'
      meta.textContent = `${nameOf(m.event.author)} · ${new Date(m.event.time).toLocaleTimeString()}${m.edited ? ' · edited' : ''}`
      const text = document.createElement('div')
      text.className = 'text'
      text.textContent = m.text
      const reactions = document.createElement('div')
      reactions.className = 'reactions'
      for (const [emoji, by] of m.reactions) {
        const b = document.createElement('button')
        b.textContent = `${emoji} ${by.size}`
        b.title = [...by].map(nameOf).join(', ')
        b.onclick = () => post('reaction', { event: m.event.id, emoji })
        reactions.append(b)
      }
      const add = document.createElement('button')
      add.textContent = '+'
      add.title = 'React'
      add.onclick = () => {
        const emoji = prompt('React with')
        if (emoji) post('reaction', { event: m.event.id, emoji })
      }
      reactions.append(add)
      if (m.event.author === me.id) {
        const edit = document.createElement('button')
        edit.textContent = 'Edit'
        edit.onclick = () => {
          const text = prompt('Edit message', m.text)
          if (text && text !== m.text) post('edit', { event: m.event.id, text })
        }
        reactions.append(edit)
      }
      li.append(meta, text, reactions)
      return li
    }),
  )
  if (atBottom) ol.scrollTop = ol.scrollHeight
}

function renderTyping() {
  const cutoff = Date.now() - 4000
  const names = [...typing].filter(([id, at]) => at > cutoff && id !== me.id).map(([id]) => nameOf(id))
  $('typing').textContent = names.length ? `${names.join(', ')} ${names.length === 1 ? 'is' : 'are'} typing…` : ''
}

function fail(err: unknown): undefined {
  statusEl.textContent = err instanceof Error ? err.message : String(err)
  setTimeout(() => (statusEl.textContent = ''), 6000)
  return undefined
}

// pickUser asks for someone: one of the people this node knows, or an
// address, <did>@<node>, of someone elsewhere.
async function pickUser(title: string): Promise<Membership | undefined> {
  await loadUsers()
  const select = $<HTMLSelectElement>('pick-user')
  const addressInput = $<HTMLInputElement>('pick-address')
  addressInput.value = ''
  const others = [...users.values()].filter(u => u.id !== me.id && !memberIDs(current ?? { id: '', kind: 'channel', heads: [] }).includes(u.id))
  select.replaceChildren(
    new Option('— someone this node knows —', ''),
    ...others.map(u => new Option(`${u.name} (${u.node.slice(0, 8)})`, u.id)),
  )
  $('pick-title').textContent = title
  pick.showModal()
  return new Promise(resolve => {
    pick.onclose = () => {
      if (pick.returnValue !== 'ok') return resolve(undefined)
      const [user, node] = addressInput.value.trim().split('@')
      if (user && node) return resolve({ user, node })
      const u = users.get(select.value)
      resolve(u ? { user: u.id, node: u.node } : undefined)
    }
  })
}

// listen keeps the page live: new events in the open channel, channels
// appearing, typing and presence.
function listen() {
  const source = new EventSource('/v1/events?ref=chat.posted&ref=chat.typing&ref=chat.presence')
  source.addEventListener('chat.posted', async ev => {
    const e: Event = JSON.parse((ev as MessageEvent).data)
    const known = channels.some(ch => ch.id === e.channel)
    if (!known || e.kind === 'invite' || e.channel === current?.id) await loadChannels()
    if (e.channel !== current?.id) return
    if (e.kind === 'invite') renderMembers()
    typing.delete(e.author)
    renderTyping()
    if (e.kind !== 'receipt') await refresh()
  })
  source.addEventListener('chat.typing', ev => {
    const { channel, user } = JSON.parse((ev as MessageEvent).data)
    if (channel !== current?.id) return
    typing.set(user, Date.now())
    renderTyping()
  })
  source.addEventListener('chat.presence', ev => {
    const { user, online: isOnline } = JSON.parse((ev as MessageEvent).data)
    if (isOnline) online.add(user)
    else online.delete(user)
    renderMembers()
  })
  source.onerror = () => (statusEl.textContent = 'Reconnecting…')
  source.onopen = () => (statusEl.textContent = '')
  setInterval(renderTyping, 1000)
  // Heartbeats keep us online, here and on the other members' nodes.
  const heartbeat = () => call('heartbeat').catch(() => {})
  heartbeat()
  setInterval(heartbeat, 30000)
}

async function main() {
  try {
    device = await Device.load()
  } catch (err) {
    fail(new Error(`This browser can't make an Ed25519 key: ${err}`))
    return
  }
  await getAuthorized()
  await login()
  const known = JSON.parse(localStorage.getItem('chat.me') ?? 'null')
  me = await ask(known?.id === device.person ? known.name : '')
  localStorage.setItem('chat.me', JSON.stringify(me))
  const show = () => {
    $('me').textContent = me.name
    $<HTMLInputElement>('address').value = address(me)
  }
  show()
  $('rename').onclick = async () => {
    me = await ask(me.name)
    localStorage.setItem('chat.me', JSON.stringify(me))
    show()
    await loadUsers()
    renderChannels()
  }
  $('copy').onclick = () => navigator.clipboard.writeText(address(me)).catch(fail)
  await loadUsers()
  await loadChannels()
  listen()

  const create = async (name: string, kind: Channel['kind'], others: Membership[]) => {
    const members = [{ user: me.id, node: me.node }, ...others]
    const ch = await submit({ author: me.id, kind: 'create', body: { name, kind, members }, time: new Date().toISOString() }).catch(fail)
    if (ch) {
      await loadChannels()
      const made = channels.find(c => c.id === ch.id)
      if (made) open(made)
    }
  }
  $('new-channel').onclick = async () => {
    const name = prompt('Channel name')
    if (name) await create(name, 'channel', [])
  }
  $('new-dm').onclick = async () => {
    const other = await pickUser('Message whom?')
    if (!other) return
    const existing = channels.find(ch => ch.kind === 'dm' && memberIDs(ch).includes(other.user))
    if (existing) open(existing)
    else await create('', 'dm', [other])
  }
  $('invite').onclick = async () => {
    const other = await pickUser(`Invite to ${label(current!)}`)
    if (other && current) await post('invite', other)
  }

  const text = $<HTMLInputElement>('text')
  let lastTyping = 0
  text.oninput = () => {
    if (!current || Date.now() - lastTyping < 2000) return
    lastTyping = Date.now()
    call('start_typing', { channel: current.id }).catch(() => {})
  }
  $<HTMLFormElement>('compose').onsubmit = async ev => {
    ev.preventDefault()
    if (!current || !text.value.trim()) return
    const body = text.value
    text.value = ''
    await post('message', { text: body })
  }
}

main().catch(fail)
