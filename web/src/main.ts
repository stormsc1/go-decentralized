import ForceGraph from 'force-graph'

// A node's report, as returned by the debug.map_network capability.
interface Report {
  id: string
  name?: string
  addrs?: string[]
  direct: boolean
  observed?: string[]
  modules?: Record<string, { capabilities?: string[]; state?: any }>
  routing?: { routing_table?: { id: string }[]; lan_peers?: { id: string }[] }
  error?: string
}

// A call or stream a node made, as returned by debug.traffic.
interface Trace {
  time: string
  kind: 'call' | 'stream'
  ref: string // the capability called, or the stream opened
  from: string
  to?: string
  addr: string
  duration: number // nanoseconds
  error?: string
}

interface Node {
  id: string
  name: string
  addrs: string[]
  direct: boolean
  observed: string[] // public IPs other nodes see it at
  nat?: string // public IP of the NAT it's behind, if any
  error: string
  modules: string[]
  knows: string[] // IDs in its routing table
  lan: string[] // IDs of its peers on the same LAN
  x?: number
  y?: number
  vx?: number
  vy?: number
}

type LinkKind = 'table' | 'lan' | 'relay'

interface Link {
  source: string | Node
  target: string | Node
  kind: LinkKind
}

const mapEveryMs = 3000
const trafficEveryMs = 1000
const flashMs = 1200 // how long a link stays lit after carrying traffic
const maxMessages = 2000
const radius = 6 // node radius, in screen pixels
const font = 'system-ui, -apple-system, "Segoe UI", sans-serif'

const graphEl = document.getElementById('graph')!
const statusEl = document.getElementById('status')!
const tableEl = document.getElementById('nodes')!
const detailsEl = document.getElementById('details')!
const tooltipEl = document.getElementById('tooltip')!
const logEl = document.getElementById('messages')!
const filterEl = document.getElementById('filter') as HTMLInputElement
const pauseEl = document.getElementById('pause') as HTMLInputElement
const countEl = document.getElementById('log-count')!

// Nodes are kept across refreshes so the layout stays put.
const nodes = new Map<string, Node>()
let links: Link[] = []
let linkKeys = new Set<string>() // node pairs a link joins
let nats = new Map<string, Node[]>() // nodes behind each NAT, by its public IP
let byAddr = new Map<string, string>() // node IDs by direct address
let shape = '' // what the graph was last fed, to skip unchanged refreshes
let fitted = false
let selected: string | null = null
let hovered: Node | null = null
let focused = new Set<string>() // the node in focus and its neighbours
let colors = readColors()

// Traffic, newest first, and when each pair of nodes last exchanged any.
const messages: Trace[] = []
const seen = new Set<string>()
const activity = new Map<string, number>()
let latest = 0 // time of the newest trace, by the clocks of the nodes

const graph = new ForceGraph<Node, Link>(graphEl)
  .backgroundColor(colors.surface)
  .autoPauseRedraw(false) // links fade out after carrying traffic
  .onRenderFramePre(beforeFrame)
  .onRenderFramePost(drawFlashes)
  .nodeCanvasObject(drawNode)
  .nodePointerAreaPaint((node, color, ctx, scale) => {
    circle(ctx, node.x ?? 0, node.y ?? 0, 12 / scale) // a 24px target
    ctx.fillStyle = color
    ctx.fill()
  })
  .linkColor(linkColor)
  .linkWidth(link => (glow(link) > 0 ? 3 : link.kind === 'relay' ? 2 : link.kind === 'lan' ? 1.5 : 1))
  .linkLineDash(link => (link.kind === 'relay' ? [4, 3] : link.kind === 'lan' ? [2, 3] : null))
  .linkCurvature(link => (link.kind === 'relay' ? 0.3 : 0)) // keeps relay links off other links
  .cooldownTime(8000)
  .onEngineStop(() => {
    if (!fitted && nodes.size > 0) {
      graph.zoomToFit(400, 60)
      fitted = true
    }
  })
  .onNodeHover(node => {
    hovered = node
    tooltipEl.hidden = !node
    if (node) fillTooltip(node)
  })
  .onNodeClick(node => select(node.id))
  .onBackgroundClick(() => select(null))

// Spread nodes out: routing tables link most nodes to each other, so only LAN
// links pull hard, holding each LAN together.
graph.d3Force('charge')?.strength(-300)
graph.d3Force('link')
  ?.distance(80)
  .strength((link: Link) => (link.kind === 'lan' ? 0.2 : 0.02))
// Give each NAT its own region around the public nodes, so the boxes don't
// overlap, and keep the public nodes in the middle.
graph.d3Force('nat', (alpha: number) => {
  const groups = [...nats.values()]
  groups.forEach((members, i) => {
    const angle = (2 * Math.PI * i) / groups.length
    const [ax, ay] = [Math.cos(angle) * 400, Math.sin(angle) * 400]
    for (const n of members) {
      n.vx = (n.vx ?? 0) + (ax - (n.x ?? 0)) * alpha * 0.4
      n.vy = (n.vy ?? 0) + (ay - (n.y ?? 0)) * alpha * 0.4
    }
  })
  for (const n of nodes.values()) {
    if (!n.nat) {
      n.vx = (n.vx ?? 0) - (n.x ?? 0) * alpha * 0.2
      n.vy = (n.vy ?? 0) - (n.y ?? 0) * alpha * 0.2
    }
  }
})

new ResizeObserver(() => graph.width(graphEl.clientWidth).height(graphEl.clientHeight)).observe(graphEl)

graphEl.addEventListener('pointermove', event => {
  tooltipEl.style.left = `${event.clientX + 12}px`
  tooltipEl.style.top = `${event.clientY + 12}px`
})

matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  colors = readColors()
  graph.backgroundColor(colors.surface)
})

filterEl.addEventListener('input', renderLog)
pauseEl.addEventListener('change', renderLog)

mapNetwork()
pollTraffic()

async function mapNetwork() {
  try {
    const res = await fetch('/v1/capabilities/debug.map_network', { method: 'POST' })
    const body: { nodes?: Report[]; message?: string } = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(body.message || res.statusText)
    update(body.nodes ?? [])
    statusEl.textContent = `${nodes.size} nodes · updated ${new Date().toLocaleTimeString()}`
    document.body.classList.remove('stale')
  } catch (err) {
    statusEl.textContent = `Can't map the network: ${err instanceof Error ? err.message : String(err)}`
    document.body.classList.add('stale')
  }
  setTimeout(mapNetwork, mapEveryMs)
}

async function pollTraffic() {
  try {
    // Start with the last two minutes, then continue from the newest trace,
    // overlapping a little as nodes record traces out of order; seen drops
    // what was already recorded.
    const since = new Date(latest ? latest - 2000 : Date.now() - 120_000).toISOString()
    const res = await fetch('/v1/capabilities/debug.traffic', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ since }),
    })
    const body: { traces?: Trace[] } = await res.json().catch(() => ({}))
    if (res.ok) record(body.traces ?? [])
  } catch {
    // mapNetwork reports connection problems.
  }
  setTimeout(pollTraffic, trafficEveryMs)
}

function update(reports: Report[]) {
  const found = new Set(reports.map(r => r.id))
  for (const id of nodes.keys()) {
    if (!found.has(id)) nodes.delete(id)
  }
  for (const r of reports) {
    const node = nodes.get(r.id) ?? ({ id: r.id } as Node)

    node.name = r.name ?? ''
    node.addrs = r.addrs ?? []
    node.direct = r.direct
    node.observed = r.observed ?? []
    node.error = r.error ?? ''
    node.modules = Object.keys(r.modules ?? {}).sort()
    node.knows = ids(r.routing?.routing_table)
    node.lan = ids(r.routing?.lan_peers)
    nodes.set(r.id, node)
  }

  const list = [...nodes.values()]
  nats = groupByNat(list)
  links = linksOf(list)
  // Feed the graph only when it changed, so the layout doesn't jolt.
  const next = JSON.stringify([list.map(n => [n.id, mode(n), n.nat]), links.map(l => [l.source, l.target, l.kind])])
  if (next !== shape) {
    shape = next
    graph.graphData({ nodes: list, links })
  }
  renderTable()
  renderDetails()
  if (hovered) fillTooltip(hovered)
}

// ids lists the IDs of contacts, sorted, as routing tables reorder whenever
// a peer is heard from.
function ids(contacts?: { id: string }[]) {
  return (contacts ?? []).map(c => c.id).sort()
}

// groupByNat groups nodes by the public IP others see them at. An IP that a
// node isn't directly reachable at belongs to a NAT, which every node seen at
// it is behind. This is an inference: carrier-grade NAT, NATs with several
// public IPs and firewalls that don't translate addresses all blur it.
function groupByNat(list: Node[]) {
  const byIP = new Map<string, Node[]>()
  for (const node of list) {
    node.nat = undefined
    const ip = node.observed[0]
    if (ip) byIP.set(ip, [...(byIP.get(ip) ?? []), node])
  }
  const groups = new Map<string, Node[]>()
  for (const [ip, members] of byIP) {
    if (members.some(n => !n.direct)) {
      groups.set(ip, members)
      for (const n of members) n.nat = ip
    }
  }
  return groups
}

// linksOf draws each routing-table entry once, however many sides know each
// other, then LAN peers not joined that way, then a relay link for every
// relay/<relay addr>/<id> address.
function linksOf(list: Node[]): Link[] {
  byAddr = new Map()
  for (const node of list) {
    for (const addr of node.addrs) {
      if (!addr.startsWith('relay/')) byAddr.set(addr, node.id)
    }
  }
  const out: Link[] = []
  const joined = new Set<string>()
  const join = (a: string, b: string, kind: LinkKind) => {
    const key = pairKey(a, b)
    if (nodes.has(b) && !joined.has(key)) {
      joined.add(key)
      out.push({ source: a, target: b, kind })
    }
  }
  for (const node of list) node.knows.forEach(id => join(node.id, id, 'table'))
  for (const node of list) node.lan.forEach(id => join(node.id, id, 'lan'))
  for (const node of list) {
    for (const addr of node.addrs) {
      const relay = addr.startsWith('relay/') ? byAddr.get(addr.split('/')[1]) : undefined
      if (relay) {
        joined.add(pairKey(node.id, relay))
        out.push({ source: node.id, target: relay, kind: 'relay' })
      }
    }
  }
  linkKeys = joined
  return out
}

function record(traces: Trace[]) {
  const now = performance.now()
  const fresh = traces.filter(t => !seen.has(traceKey(t)))
  if (!fresh.length) return
  for (const t of fresh) {
    seen.add(traceKey(t))
    latest = Math.max(latest, Date.parse(t.time))
    for (const [a, b] of hops(t)) activity.set(pairKey(a, b), now)
  }
  messages.unshift(...fresh.reverse())
  for (const t of messages.splice(maxMessages)) seen.delete(traceKey(t))
  renderLog()
}

function traceKey(t: Trace) {
  return `${t.from}|${t.time}|${t.ref}|${t.addr}`
}

// hops returns the pairs of nodes a trace's traffic crossed: sender to
// receiver, or sender to relay to receiver.
function hops(t: Trace): [string, string][] {
  if (t.addr.startsWith('relay/')) {
    const [, relayAddr, target] = t.addr.split('/')
    const relay = byAddr.get(relayAddr)
    return relay ? [[t.from, relay], [relay, t.to || target]] : []
  }
  const to = t.to || byAddr.get(t.addr)
  return to ? [[t.from, to]] : []
}

function pairKey(a: string, b: string) {
  return a < b ? `${a}|${b}` : `${b}|${a}`
}

function idOf(end: string | Node) {
  return typeof end === 'string' ? end : end.id
}

// glowOf is how lit the link between a pair of nodes is: 1 when it just
// carried traffic, fading to 0.
function glowOf(key: string) {
  const at = activity.get(key)
  return at === undefined ? 0 : Math.max(0, 1 - (performance.now() - at) / flashMs)
}

function glow(link: Link) {
  return glowOf(pairKey(idOf(link.source), idOf(link.target)))
}

// linkColor lights links up while they carry traffic, and fades the links of
// nodes out of focus.
function linkColor(link: Link) {
  const lit = glow(link)
  if (lit > 0) return rgba(colors.selection, lit)
  const base = link.kind === 'relay' ? colors.relayed : colors.link
  const inFocus = !focused.size || (focused.has(idOf(link.source)) && focused.has(idOf(link.target)))
  return inFocus ? base : rgba(base, 0.1)
}

// beforeFrame works out what's in focus: the hovered or selected node and its
// neighbours. Then it draws the NAT boxes, under everything else.
function beforeFrame(ctx: CanvasRenderingContext2D, scale: number) {
  const focus = hovered ?? (selected ? nodes.get(selected) : undefined)
  focused = new Set()
  if (focus) {
    focused.add(focus.id)
    for (const link of links) {
      const [a, b] = [idOf(link.source), idOf(link.target)]
      if (a === focus.id) focused.add(b)
      if (b === focus.id) focused.add(a)
    }
  }
  drawNats(ctx, scale)
}

// drawNats boxes the nodes behind each NAT.
function drawNats(ctx: CanvasRenderingContext2D, scale: number) {
  const px = 1 / scale
  for (const [ip, members] of nats) {
    const xs = members.map(n => n.x ?? 0)
    const ys = members.map(n => n.y ?? 0)
    // Room for the node labels, which sit under and around the nodes.
    const left = Math.min(...xs) - 48 * px
    const top = Math.min(...ys) - 20 * px
    const right = Math.max(...xs) + 48 * px
    const bottom = Math.max(...ys) + 32 * px

    ctx.beginPath()
    ctx.roundRect(left, top, right - left, bottom - top, 8 * px)
    ctx.fillStyle = colors.nat
    ctx.fill()
    ctx.lineWidth = px
    ctx.strokeStyle = colors.natBorder
    ctx.stroke()

    ctx.font = `${11 * px}px ${font}`
    ctx.textAlign = 'left'
    ctx.textBaseline = 'bottom'
    ctx.fillStyle = colors.muted
    ctx.fillText(`NAT ${ip}`, left, top - 4 * px)
  }
}

// drawFlashes lights up traffic between nodes that no link joins, e.g. nodes
// that aren't in each other's routing tables.
function drawFlashes(ctx: CanvasRenderingContext2D, scale: number) {
  for (const key of activity.keys()) {
    const lit = glowOf(key)
    if (lit === 0) {
      activity.delete(key)
      continue
    }
    const [a, b] = key.split('|').map(id => nodes.get(id))
    if (linkKeys.has(key) || !a || !b) continue
    ctx.beginPath()
    ctx.moveTo(a.x ?? 0, a.y ?? 0)
    ctx.lineTo(b.x ?? 0, b.y ?? 0)
    ctx.lineWidth = 3 / scale
    ctx.strokeStyle = rgba(colors.selection, lit)
    ctx.stroke()
  }
}

// drawNode sizes everything in screen pixels, so nodes and labels keep their
// size at any zoom.
function drawNode(node: Node, ctx: CanvasRenderingContext2D, scale: number) {
  const px = 1 / scale
  const x = node.x ?? 0
  const y = node.y ?? 0
  const r = radius * px
  ctx.globalAlpha = !focused.size || focused.has(node.id) ? 1 : 0.25

  circle(ctx, x, y, r + 2 * px) // surface ring keeps links off the node
  ctx.fillStyle = colors.surface
  ctx.fill()

  if (node.direct && !node.error) {
    circle(ctx, x, y, r)
    ctx.fillStyle = colors.direct
    ctx.fill()
  } else {
    circle(ctx, x, y, r - px)
    ctx.lineWidth = 2 * px
    ctx.strokeStyle = node.error ? colors.muted : colors.relayed
    ctx.stroke()
  }

  if (node.id === selected) {
    circle(ctx, x, y, r + 4 * px)
    ctx.lineWidth = 1.5 * px
    ctx.strokeStyle = colors.selection
    ctx.stroke()
  }

  ctx.font = `${12 * px}px ${font}`
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  ctx.fillStyle = colors.label
  ctx.fillText(label(node), x, y + r + 4 * px)
  ctx.globalAlpha = 1
}

function circle(ctx: CanvasRenderingContext2D, x: number, y: number, r: number) {
  ctx.beginPath()
  ctx.arc(x, y, r, 0, 2 * Math.PI)
}

function select(id: string | null) {
  selected = id
  renderTable()
  renderDetails()
}

// The table is the graph's accessible twin: every node, grouped by network,
// no pointer needed.
function renderTable() {
  const rows = [...nodes.values()].sort(
    (a, b) => network(a).localeCompare(network(b)) || label(a).localeCompare(label(b)),
  )
  const out: HTMLElement[] = []
  let current = ''
  for (const node of rows) {
    if (network(node) !== current) {
      current = network(node)
      const heading = document.createElement('tr')
      heading.className = 'network'
      const cell = text('th', current)
      cell.colSpan = 3
      heading.append(cell)
      out.push(heading)
    }
    const row = document.createElement('tr')
    row.setAttribute('aria-selected', String(node.id === selected))
    row.addEventListener('click', () => select(node.id))
    const knows = text('td', node.error ? '–' : String(node.knows.length))
    knows.className = 'num'
    row.append(text('td', label(node)), text('td', mode(node)), knows)
    out.push(row)
  }
  tableEl.replaceChildren(...out)
}

function renderDetails() {
  const node = selected ? nodes.get(selected) : undefined
  if (!node) {
    detailsEl.replaceChildren()
    return
  }
  const list = document.createElement('dl')
  const entry = (term: string, values: string[], code = false) => {
    list.append(text('dt', term))
    for (const value of values) {
      const dd = document.createElement('dd')
      dd.append(code ? text('code', value) : document.createTextNode(value))
      list.append(dd)
    }
  }
  entry('ID', [node.id], true)
  entry('Mode', [mode(node)])
  entry('Network', [network(node)])
  if (node.error) entry('Error', [node.error])
  entry('Addresses', node.addrs, true)
  if (node.observed.length) entry('Seen at', node.observed, true)
  if (!node.error) {
    entry('Routing table', [node.knows.map(nameOf).join(', ') || 'empty'])
    entry('LAN peers', [node.lan.map(nameOf).join(', ') || 'none'])
    entry('Modules', [node.modules.join(', ')])
  }
  detailsEl.replaceChildren(text('h2', label(node)), list)
}

function renderLog() {
  if (pauseEl.checked) return
  const query = filterEl.value.trim().toLowerCase()
  const matching = query ? messages.filter(t => describe(t).includes(query)) : messages
  logEl.replaceChildren(...matching.slice(0, 500).map(logRow))
  countEl.textContent =
    `${matching.length} message${matching.length === 1 ? '' : 's'}` +
    (matching.length > 500 ? ' · showing the newest 500' : '')
}

function logRow(t: Trace) {
  const row = document.createElement('tr')
  if (t.error) row.className = 'failed'
  row.addEventListener('click', () => select(t.from))
  const took = text('td', `${(t.duration / 1e6).toFixed(1)} ms`)
  took.className = 'num'
  const result = text('td', t.error ? `✕ ${t.error}` : 'ok')
  result.className = 'result'
  result.title = t.error ?? ''
  row.append(
    text('td', clock(t.time)),
    text('td', nameOf(t.from)),
    text('td', t.to ? nameOf(t.to) : t.addr),
    text('td', t.kind === 'stream' ? `${t.ref} (stream)` : t.ref),
    text('td', t.addr.startsWith('relay/') ? 'relay' : 'direct'),
    took,
    result,
  )
  return row
}

// describe is what the log filter searches.
function describe(t: Trace) {
  return [nameOf(t.from), t.to ? nameOf(t.to) : t.addr, t.ref, t.error ?? ''].join(' ').toLowerCase()
}

function clock(time: string) {
  const d = new Date(time)
  return `${d.toTimeString().slice(0, 8)}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

function fillTooltip(node: Node) {
  tooltipEl.replaceChildren(
    text('strong', label(node)),
    text('span', `${mode(node)} · ${network(node)}`),
    text('span', `${node.id.slice(0, 16)}…`),
    ...node.addrs.map(addr => text('code', addr)),
  )
}

// label shortens names like "gl-organization-node" to "gl".
function label(node: Node) {
  return node.name.replace(/-(organization-)?node$/, '') || node.id.slice(0, 8)
}

function nameOf(id: string) {
  const node = nodes.get(id)
  return node ? label(node) : id.slice(0, 8)
}

function mode(node: Node) {
  if (node.error) return 'Unreachable'
  return node.direct ? 'Direct' : 'Through a relay'
}

// network names where a node sits, as far as the explorer can tell.
function network(node: Node) {
  if (node.nat) return `NAT ${node.nat}`
  if (node.error) return 'Unreachable'
  return node.direct ? 'Public' : 'Behind a NAT'
}

// text creates an element showing untrusted text: names, addresses and
// errors come from other nodes.
function text<K extends keyof HTMLElementTagNameMap>(tag: K, content: string) {
  const el = document.createElement(tag)
  el.textContent = content
  return el
}

// rgba returns color (#rrggbb or rgba()) with its opacity scaled by alpha.
function rgba(color: string, alpha: number) {
  const hex = color.match(/^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i)
  const [r, g, b, a = 1] = hex ? hex.slice(1).map(h => parseInt(h, 16)) : (color.match(/[\d.]+/g) ?? []).map(Number)
  return `rgba(${r}, ${g}, ${b}, ${a * alpha})`
}

function readColors() {
  const style = getComputedStyle(document.documentElement)
  const color = (name: string) => style.getPropertyValue(name).trim()
  return {
    surface: color('--surface'),
    label: color('--text-secondary'),
    muted: color('--text-muted'),
    selection: color('--text-primary'),
    link: color('--link'),
    direct: color('--direct'),
    relayed: color('--relayed'),
    nat: color('--nat'),
    natBorder: color('--border'),
  }
}
