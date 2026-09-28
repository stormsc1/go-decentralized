// Identity in the browser: you are your root, a did:key; this browser holds a
// device key the root authorized, and signs with it. The root lives in the
// vault, wrapped so the vault can't read it: from your passkey's PRF secret
// (or a passphrase) the browser derives a wrapping key, which never leaves
// it, and a proof, whose hash names your vault account. Signing in on a new
// device fetches the wrapped root, unwraps it in memory, authorizes the
// device key and forgets the root. See docs/design/identity-auth.md and
// spec/identity.md.

// Signed is data someone signed, as spec/identity.md carries it.
export interface Signed {
  data: string // base64
  signer: string
  sig: string // base64
  delegation?: Signed
}

const encoder = new TextEncoder()
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

export const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
export const unbase64 = (s: string) => Uint8Array.from(atob(s), c => c.charCodeAt(0))

// didKey returns the did:key of a raw Ed25519 public key.
const didKey = (pub: Uint8Array) => 'did:key:z' + base58(new Uint8Array([0xed, 0x01, ...pub])) // ed25519-pub

// signWith signs v for purpose with key, as nodes do: over
// "decentralized-signature" NUL purpose NUL data (spec/modules.md, "Signing").
async function signWith(key: CryptoKey, signer: string, purpose: string, v: unknown, delegation?: Signed): Promise<Signed> {
  const data = encoder.encode(JSON.stringify(v))
  const message = new Uint8Array([...encoder.encode('decentralized-signature\0'), ...encoder.encode(purpose), 0, ...data])
  const sig = new Uint8Array(await crypto.subtle.sign('Ed25519', key, message))
  const s: Signed = { data: base64(data), signer, sig: base64(sig) }
  if (delegation) s.delegation = delegation
  return s
}

// --- Browser storage ---------------------------------------------------------

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

// --- The device ----------------------------------------------------------------

// Device is this browser's key, and the root's authorization of it.
export class Device {
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
    return new Device(didKey(raw), keys, await idbGet<Signed>('authorization'))
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

  // authorize takes an authorization a root holder made for this device.
  async authorize(a: Signed): Promise<void> {
    if (!a.data || !a.signer || !a.sig) throw new Error('not an authorization')
    this.authorization = a
    if (!this.authorized()) {
      this.authorization = undefined
      throw new Error("this authorization isn't for this device, or has expired")
    }
    await idbPut('authorization', a)
  }

  // sign signs v for purpose, for the root, carrying its delegation.
  sign(purpose: string, v: unknown): Promise<Signed> {
    return signWith(this.keys.privateKey, this.did, purpose, v, this.authorization)
  }
}

// --- The vault -----------------------------------------------------------------

// WrappedRoot is what the vault keeps: the root's public key, and its private
// key encrypted under the wrapping key.
interface WrappedRoot {
  v: 1
  pub: string // base64, raw Ed25519 public key
  iv: string // base64
  ct: string // base64, AES-GCM of the PKCS#8 private key
}

// Vault is the vault module, through the node the app is served from: its
// own, or the one on the node named, which it forwards to.
export class Vault {
  constructor(readonly node?: string) {}

  // remembered is the node of the vault this browser last signed in with.
  static remembered(): Promise<string | undefined> {
    return idbGet<string>('vault')
  }

  private async call<T>(name: string, input: object): Promise<T> {
    const res = await fetch(`/v1/capabilities/vault.${name}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(this.node ? { ...input, vault: this.node } : input),
    })
    const body = await res.json()
    if (!res.ok) throw Object.assign(new Error(body.message ?? res.statusText), { code: body.code })
    return body
  }

  // open returns the account's blob, or undefined if there's no account.
  async open(proof: Uint8Array): Promise<WrappedRoot | undefined> {
    try {
      return (await this.call<{ blob: WrappedRoot }>('open', { proof: base64(proof) })).blob
    } catch (err) {
      if ((err as { code?: string }).code === 'not_found') return undefined
      throw err
    }
  }

  // save keeps the wrapped root under the proof, signed by the root itself.
  async save(root: CryptoKey, rootDID: string, proof: Uint8Array, blob: WrappedRoot): Promise<void> {
    await this.call('save', { signed: await signWith(root, rootDID, 'vault.save', { proof: base64(proof), blob }) })
  }
}

// secrets derives the wrapping key and the proof from a 32-byte secret only
// the person can produce.
async function secrets(secret: Uint8Array<ArrayBuffer>): Promise<{ wrap: CryptoKey; proof: Uint8Array }> {
  const material = await crypto.subtle.importKey('raw', secret, 'HKDF', false, ['deriveKey', 'deriveBits'])
  const salt = encoder.encode('go-decentralized vault')
  const wrap = await crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt, info: encoder.encode('wrap') },
    material,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  )
  const proof = new Uint8Array(await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt, info: encoder.encode('proof') }, material, 256))
  return { wrap, proof }
}

// authorizeDevice has the root authorize the device for scope, for days,
// with the root unwrapped in memory only for this. With create, the person
// is new: a root is made and kept in the vault first.
async function authorizeDevice(device: Device, vault: Vault, secret: Uint8Array<ArrayBuffer>, create: boolean, scope = '*', days = 30): Promise<void> {
  const { wrap, proof } = await secrets(secret)
  let wrapped = await vault.open(proof)
  let root: CryptoKey
  let pub: Uint8Array
  if (wrapped) {
    pub = unbase64(wrapped.pub)
    const pkcs8 = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: unbase64(wrapped.iv) }, wrap, unbase64(wrapped.ct))
    root = await crypto.subtle.importKey('pkcs8', pkcs8, { name: 'Ed25519' }, false, ['sign'])
  } else if (create) {
    const keys = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair
    pub = new Uint8Array(await crypto.subtle.exportKey('raw', keys.publicKey))
    const pkcs8 = new Uint8Array(await crypto.subtle.exportKey('pkcs8', keys.privateKey))
    const iv = crypto.getRandomValues(new Uint8Array(12))
    const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, wrap, pkcs8))
    wrapped = { v: 1, pub: base64(pub), iv: base64(iv), ct: base64(ct) }
    root = keys.privateKey
    await vault.save(root, didKey(pub), proof, wrapped)
  } else {
    throw new Error("this vault holds no identity for that; create one, or sign in where you made it")
  }
  const grant = { device: device.did, expires: new Date(Date.now() + days * 86_400_000).toISOString(), scope }
  await device.authorize(await signWith(root, didKey(pub), 'did.delegation', grant))
  await idbPut('vault', vault.node ?? '')
}

// --- Ways to produce the secret -----------------------------------------------

const prfSalt = encoder.encode('go-decentralized vault')

// passkeySecret evaluates the passkey's PRF: the same 32 bytes every time for
// the same passkey, which syncs between the person's devices. create makes a
// new passkey first.
async function passkeySecret(create: boolean): Promise<Uint8Array<ArrayBuffer>> {
  const rpId = location.hostname
  const prf = (cred: Credential | null): Uint8Array<ArrayBuffer> | undefined => {
    const results = (cred as PublicKeyCredential | null)?.getClientExtensionResults() as any
    const first = results?.prf?.results?.first
    return first ? new Uint8Array(first) : undefined
  }
  if (create) {
    const cred = await navigator.credentials.create({
      publicKey: {
        rp: { name: 'go-decentralized', id: rpId },
        user: { id: crypto.getRandomValues(new Uint8Array(16)), name: 'identity', displayName: 'Your identity' },
        challenge: crypto.getRandomValues(new Uint8Array(32)),
        pubKeyCredParams: [
          { type: 'public-key', alg: -8 },
          { type: 'public-key', alg: -7 },
        ],
        authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
        extensions: { prf: { eval: { first: prfSalt } } } as any,
      },
    })
    const results = (cred as PublicKeyCredential | null)?.getClientExtensionResults() as any
    if (!results?.prf?.enabled) throw new Error("this passkey can't derive a secret (no PRF); use a passphrase instead")
    const secret = prf(cred)
    if (secret) return secret
    // Some authenticators evaluate the PRF only on assertion: ask once more.
  }
  const cred = await navigator.credentials.get({
    publicKey: {
      challenge: crypto.getRandomValues(new Uint8Array(32)),
      rpId,
      userVerification: 'required',
      extensions: { prf: { eval: { first: prfSalt } } } as any,
    },
  })
  const secret = prf(cred)
  if (!secret) throw new Error("this passkey can't derive a secret (no PRF); use a passphrase instead")
  return secret
}

// passphraseSecret derives the secret from a passphrase and a handle, for
// browsers without PRF: slow on purpose.
async function passphraseSecret(handle: string, passphrase: string): Promise<Uint8Array<ArrayBuffer>> {
  const material = await crypto.subtle.importKey('raw', encoder.encode(passphrase.normalize('NFKC')), 'PBKDF2', false, ['deriveBits'])
  const bits = await crypto.subtle.deriveBits(
    { name: 'PBKDF2', hash: 'SHA-256', salt: encoder.encode('go-decentralized vault:' + handle.trim().toLowerCase()), iterations: 600_000 },
    material,
    256,
  )
  return new Uint8Array(bits)
}

// signIn has the root authorize the device, one of the ways there are.
export const signIn = {
  // withPasskey uses the person's passkey; with create, makes a passkey and
  // an identity first.
  async withPasskey(device: Device, vault: Vault, create: boolean): Promise<void> {
    await authorizeDevice(device, vault, await passkeySecret(create), create)
  },
  async withPassphrase(device: Device, vault: Vault, handle: string, passphrase: string, create: boolean): Promise<void> {
    if (handle.trim().length < 3 || passphrase.length < 12) throw new Error('a handle of 3 characters or more, and a passphrase of 12 or more')
    await authorizeDevice(device, vault, await passphraseSecret(handle, passphrase), create)
  },
  // withAuthorization takes one a root holder made, e.g. the CLI's.
  async withAuthorization(device: Device, pasted: string): Promise<void> {
    await device.authorize(JSON.parse(pasted))
  },
}
