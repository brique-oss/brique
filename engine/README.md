# Brique engine

The Go engine of Brique: it runs a tree of contexts, routes messages between them,
executes capacities (DSL, compiled and interpreted wrappers), stores matters, and, when
configured, talks to other instances over signed HTTPS.

Security and permissions have one document: **[SECURITY.md](SECURITY.md)**. Everything
about who may do what, the signed protocol, keys and policy generations, running
`brique-sec`, and what has been proven is there.

## Layout

One Go module (`brique_engine`) at the repository root. Paths are relative to the repository root. The
dependency rule is enforced by `engine/architecture_test.go`: `contracts` import only the standard library;
`platform` imports `contracts` and `golang.org/x/sys` only; no component imports another component's internals.

| Directory | What it holds |
|---|---|
| `cmd/brique/` | the engine's process entry: flags, security start-up, signals, shutdown |
| `cmd/brique-sec/` | offline administration of the instance's security |
| `cmd/brique-launcher/`, `cmd/brique-web/`, `cmd/brique-browser-host/` | the launcher, the public web host and the browser native host (composition only) |
| `contracts/` | formats shared by several components, standard library only: `directoryobject`, `buildinfo`, `publication`, `webprofile`, `instanceselection`, `connectiondiagnosis` |
| `platform/` | operating-system primitives: `filesystem` (atomic files), `protectedfile`, `locking`, `storage` (free disk), `listener` (accept back-off) |
| `engine/context/` | the context tree and its loops |
| `engine/communication/` | routing, interfaces (ws, http, https), signed ingress and egress |
| `engine/junction/` | `Delivery` and the authority it carries, wrapper secrets and delegation, trace emission |
| `engine/execution/`, `engine/matter/`, `engine/reflexive/`, `engine/trace/` | the families, each with its own door |
| `engine/security/` | the security subsystem (package map in SECURITY.md, appendix B) |
| `engine/transport/` | `networkpolicy`, `signedtransport`, `browsertransport`, `browserclient`: the signed engine-to-engine transport; `diagnostic`: the connection diagnosis of a declared peer |
| `engine/circulation/`, `engine/configuration/` | the message and trace wire types and the configuration keys and values used by the families |
| `engine/shared/` | instance files, element names, control plane and the engine's own concepts |
| `engine/enginebuild/` | the engine's build provenance |
| `engine/localruntime/`, `engine/browserbridge/` | local engine descriptor, discovery and `brique.local-control`; the browser bridge protocol and its IPC |
| `engine/template/`, `engine/sandbox/` | the canonical `context.json` and capacity templates; fixtures used by the integration tests |
| `launcher/` | instance registry, supervisor, runtime package, launcher control, the `InstanceResolver` |
| `spark/hosts/web/` | the web host: static server and WebSocket proxy (Go) next to the TypeScript publisher |
| `spark/hosts/browser/` | the browser host: native-messaging host (`host/`, Go), the extension (`extension/`) and the page bridge (TypeScript) |
| `integration/` | tests that cross components or executables |

## Build and run

```sh
go build ./...                      # engine, brique-sec, launcher, web host and browser host
go build -o brique-engine ./cmd/brique       # from the repository root
go build -o brique-sec ./cmd/brique-sec

./brique-engine -root_path <instance>/root     # an instance runs locally with no further setup
```

`<instance>` is the directory that holds `root/` next to `wrapper/` and the rest. Flags: `-root_path`,
`-security_dir` (see below), `-security_generation` (first activation only), `-runtime_dir` (private
local discovery/control override), `-websocket_addr` (loopback launch override such as
`127.0.0.1:0`), `-audit_max_file_mib`
(default 64), `-audit_retention_days` (default 14).

`brique-engine -build_info_json` prints the versioned `brique.build-info/1` contract and exits without
opening an instance. It reports engine/toolchain/platform and VCS provenance when embedded by Go or
injected by the packager; a development build with no revision leaves those optional fields absent.

At startup the engine resolves the physical root and takes an exclusive kernel lock on the root
directory object and, when configured, on the machine security directory before it opens security
state or Contexts. A second engine using either object is refused. The lock does not create a file in
the root, so a read-only root can run. `engine.pid` remains a second mandatory compatibility lock for
security-enabled instances and for `brique-sec apply --reload`.

Every running engine also publishes `brique.engine-runtime/2` in its private runtime directory (readers retain `/1` compatibility) and
serves `brique.local-control/1` over an owner-only Unix socket or Windows named pipe. The fixed
operations are `status`, `version`, `root`, `generation` and ordered `stop`; every request must name the
current random generation. The default Unix directory is `/tmp/brique-runtime-<uid>` so the socket path
stays portable; `-runtime_dir` selects another absolute protected directory.
The effective WebSocket address, including its allocated port when `:0` is used, is published in the
runtime descriptor without rewriting the instance's `context.json`.

The separate public contract `brique.instance-descriptor/1` now has a strict builder/verifier in
`security/instancedescriptor`. It is self-signed through the active signer and expires after at most
five minutes. `security/runtime.AuthenticateInstanceDescriptor` is the required identified-mode gate:
it also checks the current Brique Sec key, principal, audience, browser origin and key-to-machine
binding. Advertised endpoints remain proposals and must pass network policy before dialing. The
configured HTTPS interface publishes this contract and the browser adapter consumes it.

`security/browserhello` implements the matching `brique.browser-wss/1` three-message handshake.
`security/runtime` constructs client/server state only through the current signer and trust snapshot,
and rechecks the initiator before establishment. The handshake is memory-only and produces a
transcript-bound `bwg1_…` connection generation. `browsertransport` now runs this handshake over a
real WebSocket: dedicated subprotocol, no browser Origin, bounded concurrency/deadlines and outbound
dial pinned to the checked IP set. Its post-hello binary framing carries byte-identical
`security/wire` envelopes in order and acknowledges only after remote ingress accepts them; it is
bounded and never retries. `signedtransport` now supplies the bounded, transport-neutral route hub:
Comm verifies socket pair/key and the reserved `brique.ui` actor, selects an active route only for UI
traffic, and returns responses on the exact admitted `bwg1_`. `browsertransport.RunHubRoute` derives
the remote pair from the signed transcript and owns registration for the exact socket lifetime. A
root `outerCtx` HTTPS interface can enable `browser_wss`: the engine then publishes the fresh signed
descriptor at `<path>/descriptor`, serves the engine-to-engine socket at `<path>/ui`, requires TLS 1.3
and ties all sessions to interface shutdown. External WSS URL and exact UI origins are declared in
configuration, never inferred from listener or request data. `browserclient` now implements the
initiating composition from bridge-attested descriptor/origin through current Brique Sec, pinned
network resolution, signed hello and route registration. The remote bridge Sink calls that connector,
binds one channel to the authenticated peer/machine, forces actor `brique.ui`, feeds Comm and sends
deliveries back only through that channel. The configured root mounts its private engine IPC and
publishes the endpoint in local discovery. `brique-browser-host` resolves only the launcher-selected
running instance, binds its generation, runs the K2 Session on Chrome native framing and never reads a
Brique key. The launcher now installs the native manifest for one exact Chromium extension ID from the
selected instance's verified packaged runtime. The packaged MV3 extension relays opaque payloads,
derives origin/document/frame from Chrome, and waits for trusted user activation.

The connector is exercised end to end with two mutually configured Brique Sec runtimes, a verified
TLS 1.3 server certificate, an IP-pinned WSS dial, the signed three-message hello and duplex hub
traffic. It also rechecks the atomically published Brique Sec runtime after the network handshake and
before route registration: a reload during the dial closes the socket and requires an explicit new
open instead of publishing authority derived from the superseded snapshot.

The remote Browser Sink is also exercised through the complete Comm round trip. It discards the
page's source and identity, keeps only the semantic user type, and root ingress manufactures the
registered `@ui_<channel>` origin before signing with actor `brique.ui`. A second Brique Sec runtime
verifies the intention over the real pinned TLS route; its signed final response is then verified and
delivered only to the originating browser channel. Root and child UI ingress therefore apply the same
engine-owned source rule.

A Chrome for Testing 157 campaign on macOS now exercises that remote path outside the Go harness:
same-origin descriptor fetch, trusted click, real native-host launch, instance A signature, pinned
TLS 1.3 to instance B, `read.state`, and the final response back to the page. B authorizes the
intersection of the instance principal and `brique.ui`; page-forged source and identity do not cross
the bridge. The campaign also found a descriptor-renewal edge: the HTTP request context has no
deadline, while Brique Sec correctly requires one for signing. The handler now supplies a bounded
five-second signing context, covered by a forced-expiry regression test.

`networkpolicy` and `security/runtime.ResolveBrowserRoute` implement the outbound destination gate.
The descriptor key selects one configured peer machine, the advertised WSS path must remain under that
machine's signed HTTPS target, every DNS answer must be allowed, and the resulting dial function uses
the checked IP literals without a second lookup. Private/LAN ranges require explicit prefixes;
cleartext WS is limited to explicitly enabled loopback. `security/instancedescriptor.Publisher` keeps
the current short-lived descriptor atomic in memory and refreshes it for time, key or configuration
changes without any durable generation counter.

## Launcher registry

`brique-launcher` maintains a private, versioned machine registry. Registering an instance resolves
the filesystem identity of its root and asks the selected engine binary for `brique.build-info/1`;
the launcher does not parse Brique security policy and does not hold an instance key.

```sh
brique-launcher instance add --alias demo --root /path/to/instance/root --engine /path/to/brique-engine
brique-launcher instance add --alias packaged --root /path/to/other/root --runtime /path/to/brique-runtime \
  --publication /path/to/immutable-root-publication
brique-launcher instance list --json
brique-launcher instance show demo
brique-launcher browser gateway set demo
brique-launcher browser gateway show --json
brique-launcher browser gateway clear
brique-launcher instance remove demo
```

Use `--data-dir` before `instance` to select a different private launcher directory. Supervision is
declared as `managed` (default) or `external`; external instances cannot request launcher autostart.
Removing a registry entry never removes the root, security directory, runtime or logs. Process
supervision runs as a persistent foreground service and is controlled through an owner-only local
socket (or an ACL-protected named pipe on Windows):

```sh
brique-launcher serve
brique-launcher instance start demo
brique-launcher instance status demo --json
brique-launcher instance restart demo
brique-launcher instance stop demo
brique-launcher instance update-engine demo --engine /path/to/another/brique-engine
brique-launcher instance update-runtime packaged --runtime /path/to/another/brique-runtime
brique-launcher instance update-publication packaged --publication /path/to/another-publication
brique-launcher instance update-publication packaged --clear
brique-launcher shutdown
```

The browser gateway choice is a separate protected, revisioned machine setting. A web page cannot
provide the alias, and removing the selected instance makes the choice unavailable rather than
silently selecting another instance. A running native host keeps the alias and revision captured at
startup; changing the choice affects only later hosts and cannot move an active channel. The selected
root object, control PID/generation and IPC hello generation must all agree, so a reachable endpoint
from another generation is refused before Sink access. The strict Go/TypeScript session schema is
`brique.browser.bridge/1`: the native host owns session/channel identifiers, binds an activated
document/frame to one canonical origin and closes every channel when the native port disappears.
Its remote engine Sink is implemented: it opens the authenticated WSS route, forces the exact
peer/machine and `brique.ui` actor, and returns Comm deliveries to the originating channel. The
owner-only `brique.browser-engine/1` IPC client/listener and runtime-descriptor endpoint are also
implemented, with generation binding, strict sequences and EOF teardown. Engine startup mounts and
publishes it only when the root explicitly enables `communication.browser_bridge`; the network profile
is fail-closed and separate from remote signatures. The packaged native host consumes that IPC and
implements `brique.browser.bridge/1`; the runtime advertises both contracts. The launcher installs a
native-host manifest naming exactly the requested extension ID for Chrome, Chromium or Brave, or the
fixed Gecko ID for Firefox. The runtime carries separate unpacked Chromium-family and Firefox MV3
packages. These four browsers are the complete extension support perimeter. Native-host
installation deliberately requires the default launcher data directory: Chrome cannot pass a custom
`--data-dir` back to the host it starts.

```sh
brique-launcher browser gateway set packaged
brique-launcher browser host install --browser brave --extension-id <32-character-id>
brique-launcher browser host install --browser chrome --extension-id <32-character-id>
brique-launcher browser host install --browser chromium --extension-id <32-character-id>
brique-launcher browser host install --browser firefox --extension-id brique-browser-bridge@brique.dev
```

HTTPS pages must provide a signed descriptor through the same-origin
`/.well-known/brique/instance-descriptor` link emitted by Spark. The extension never accepts a remote
HTTPS open without it. A descriptor-free local open is accepted only when its extension-attested
origin exactly matches the launcher-supervised `brique-web` URL of the selected gateway; another
loopback origin is refused. The engine then re-admits `local.ui` for every message and binds replies
to the exact channel/UI. Chrome for Testing 157 campaigns on macOS cover real injection, native
host launch, hello, trusted activation, the pre-adapter refusal, then a complete local `matter.read`
whose `ok` response reached the exact `@local_ui` channel with page-forged identity removed. They also
found and fixed strict normalization of the launcher root URL with its trailing slash. Iframes,
navigation and MV3/native-host losses are covered by the lifecycle campaign below. A composed remote
test crosses the K2 Session, real private IPC socket, BrowserSink, Comm, mutually configured Brique
Sec runtimes and pinned TLS 1.3 in both directions. A fourth real-browser campaign covers the same
remote path from Chrome injection and activation through native-host launch to the returned
`read.state` response. Windows-native ACLs and other browser/platform combinations remain to qualify.

The content relay now defers `connectNative()` itself until a trusted active user event releases an
actual open request. Installing the extension therefore does not spawn a Brique process on ordinary
pages. A second real-browser lifecycle campaign verifies zero host before activation, fresh PID and
channel after navigation, iframe denial, fail-closed native-host loss, forced service-worker loss and
successful re-admission only from a new document with another trusted click.

The supervisor revalidates root/security object identities and engine provenance before launch, waits
for authenticated local-control readiness, serializes commands per alias and performs bounded ordered
stops. It observes `external` instances but never starts, adopts or stops them. Engine logs are private,
checked every 250 ms and reset above 4 MiB; each alias keeps at most 10 files, 14 days and 32 MiB.
Recognizable private-key blocks, JSON secret/token/password/authorization fields and Bearer values are
redacted atomically after exit. Arbitrary unlabelled secrets cannot be inferred from free-form text.
Managed engines receive an inherited lifetime pipe: a hard launcher exit produces EOF and follows the
engine's ordered shutdown path instead of leaving an orphan.

`restart` holds the same per-instance command lock across stop and start. Registry format
`brique.launcher-registry/3` stores a package path with its `brt1_…` ID and, optionally, an immutable
web publication path with its `spub1_…` ID. Formats `/1` and `/2` are migrated atomically while holding
the registry lock. A managed start revalidates the complete package and publication before
executing its engine. `update-runtime` is explicit and requires a stopped instance. `update-engine` is explicit,
requires the instance to be stopped, validates the new binary's build-info and platform, and only then
updates the registry revision, clearing any package association. A publication requires a runtime
declaring both Spark web contracts and a `web-host` component. The supervisor starts that host only
after engine readiness, reports its loopback URL, stops it before the engine, and retains an explicit
failed state if it exits unexpectedly. Neither command builds, downloads,
checks out or automatically starts code;
those distribution concerns belong to the runtime builder lot.

## Runtime package

From the repository root, a complete local runtime can be built into a new directory:

```sh
npm run build:runtime -- --output /path/to/runtimes/brique-darwin-arm64
/path/to/runtimes/brique-darwin-arm64/bin/brique-launcher runtime inspect \
  /path/to/runtimes/brique-darwin-arm64 --json
```

The builder stages the package beside its destination, builds `brique`, `brique-sec`,
`brique-launcher`, the separate Go host `brique-web` and the keyless `brique-browser-host`, copies the compiled Spark/Pulse, editor UI and browser-extension assets, creates
`brique.runtime/1`, then asks the packaged launcher to validate it before an atomic rename.
An existing destination is never overwritten. The manifest records source provenance, exact
toolchains, platform, engine build information, local contracts and every packaged file's size and
SHA-256. Its `brt1_…` identifier is recomputed by the launcher from that inventory; changing the ID,
adding/removing a file, altering bytes or introducing a symlink is refused.

Spark/Pulse remain versioned browser/build assets. `brique-web` only serves an already prepared,
immutable publication; it does not run Spark, resolve Intentions or hold Brique keys. Before listening,
it verifies legacy `brique.spark-publication/1` snapshots and the current strict
`brique.spark-publication/2` inventory, `brique.spark-ui-catalog/1`, every routed
`brique.spark-web-bootstrap/1` payload and their logical identity relationships. It currently
binds explicit loopback addresses only. Its managed launcher lifecycle and proxy to the dedicated
engine-owned `public.web` ingress are implemented; remote TLS remains a separate server contract.
Source worktrees, `node_modules` development trees, instances, keys, Matters and logs
are not runtime content.

When a launcher-managed instance has a publication, the launcher supplies the same immutable directory
to the engine with `-web_publication`. The engine revalidates it, then Reflexive `ui.list` and `ui.open`
return only the intersection of published UIs, active Context routes and current read admission. The
static catalog is not served over HTTP and grants no authority.

From the repository root, build a new publication and serve it:

```sh
npm run publish:web -- --context /path/to/instance/root/context.json --output /path/to/new-publication
brique-web -publication /path/to/new-publication -addr 127.0.0.1:0
```

The browser can render that snapshot immediately. Under the launcher it sends only the Intentions
authorized to `public.web` through a same-origin proxy; `brique-web` owns neither admission nor keys.
An extension bridge may replace that authority after trusted activation. A standalone host without
`-public_web_backend` remains static-only. The publication never embeds the historical engine WebSocket.

## Security in short

An instance talks to other instances over signed HTTPS only once it has been given a name and keys:

```sh
brique-sec --root <instance>/root init --instance <name>                 # once
brique-sec --root <instance>/root key show                                # what to give a peer
brique-sec --root <instance>/root tls init --host <name or address> --addr 0.0.0.0:8443   # HTTPS certificate + interface
brique-sec --root <instance>/root tls trust <peer's certificate.pem>
brique-sec --root <instance>/root principal add <peer> --kind instance --key <its public key>
brique-sec --root <instance>/root grant add <peer> call '/root/tools#capacity:*'
brique-sec --root <instance>/root peer add <peer> --audience <its name> --interface outerCtx --target https://host:8443/brique
brique-sec --root <instance>/root apply --reload                          # nothing is active before this
```

- **What is where.** `<instance>/security/access.json` — who may do what, with the peers — is part of the
  instance and is versioned with it; edit it with the commands or by hand, then `apply`. The private keys,
  the signed policy and the durable state are in `~/.brique/<name>` on the machine and never versioned. The
  engine finds both from `-root_path` alone (`-security_dir` or `BRIQUE_SECURITY_DIR` name another
  machine directory).
- **A lost machine** is replaced by checking the instance out and running `init` again: new keys, the same
  principals, grants and peers. Peers must register the new public key.
- **Secrets of the wrappers.** A provider key is kept on the machine with `brique-sec secret add <name>`
  (the value is read on the standard input, never an argument), granted to a wrapper in `access.json`
  (`"secrets"`) and declared by that wrapper (`run.secrets` in its `context.json`). The engine puts it in the
  environment of that wrapper at launch and of no other. See [SECURITY.md](SECURITY.md) §8.6 and §11.3.
- **More:** the step-by-step recipes (connecting two instances, editing by hand, replacing a machine) are
  in [SECURITY.md](SECURITY.md) §11.3, the layout in §11.1, the model and what has been proven in the rest.

Platforms: linux, darwin and windows. The windows code is cross-compiled and vetted
(`GOOS=windows go vet ./...`); it has not yet been run on a windows machine.

## Test

```sh
go test ./...                       # everything; the integration tests start real engines
go test -race ./...
GOOS=windows go vet ./...           # type-checks the windows code and its tests
```

Some tests need `python3` and a C++ compiler (`c++`, with libcurl) and skip if they are absent. The
end-to-end tests under `context/engine_real_*` build the engine and `brique-sec`, start separate engines
as processes and drive them for real; the second one uses Spark and Pulse as the user interface and needs
`node` and the repository's `npm install` (`go test ./engine/context -run TestRealProcesses`).

* **Fuzz targets** (run each for a while, not only the seed corpus):
  `go test ./engine/security/wire -run xxx -fuzz FuzzVerifyClear`, and likewise `FuzzBootstrapDocument`,
  `FuzzActivationStateLoad`, `FuzzJournalRecovery`, `FuzzIdentifiers`, `FuzzProtectedRelativePath`,
  `FuzzSignedIngressHandler`, `FuzzParseTarget`, `FuzzStrictJSONAndCanonicalization`.
* **Crash campaigns** kill a child process at random instants and check what is left:
  `go test ./engine/security/activation ./engine/security/replay ./engine/security/secadmin -run CrashCampaign`.
* **Benchmarks**: `go test ./engine/security/... ./engine/junction -run xxx -bench .`
* **Wrapper protocol vectors** are shared with the wrapper libraries:
  `templates/instance/wrapper/vectors/`.

## Keeping SECURITY.md true

The capacity matrix in SECURITY.md is generated from the code. If a class or an extractor
changes, `go test ./engine` fails; regenerate with

```sh
go test ./engine -run SecurityDocCapacityMatrix -update-security-doc
```

and read the diff: it is the change in what the engine allows.
