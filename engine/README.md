# Brique engine

The Go engine of Brique: it runs a tree of contexts, routes messages between them,
executes capacities (DSL, compiled and interpreted wrappers), stores matters, and, when
configured, talks to other instances over signed HTTPS.

Security and permissions have one document: **[SECURITY.md](SECURITY.md)**. Everything
about who may do what, the signed protocol, keys and policy generations, running
`brique-sec`, and what has been proven is there.

## Layout

| Directory | What it holds |
|---|---|
| `main.go` | process entry: flags, security start-up, signals, shutdown |
| `context/` | the context tree and its loops |
| `communication/` | routing, interfaces (ws, http, https), signed ingress and egress |
| `junction/` | `Delivery` and the authority it carries, wrapper secrets and delegation, trace emission |
| `execution/`, `matter/`, `reflexive/`, `trace/` | the families, each with its own door |
| `security/` | the security subsystem (package map in SECURITY.md, appendix B) |
| `cmd/brique-sec/` | offline administration of the instance's security |
| `shared/` | files, atomic replace, protected files (unix and windows) |
| `template/` | the canonical `context.json` and capacity templates |
| `sandbox/` | fixtures used by the integration tests |

## Build and run

```sh
go build ./...                      # the engine and brique-sec
go build -o brique-engine .
go build -o brique-sec ./cmd/brique-sec

./brique-engine -root_path <instance>/root     # an instance runs locally with no further setup
```

`<instance>` is the directory that holds `root/` next to `wrapper/` and the rest. Flags: `-root_path`,
`-security_dir` (see below), `-security_generation` (first activation only), `-audit_max_file_mib`
(default 64), `-audit_retention_days` (default 14).

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
as processes and drive them for real (`go test ./context -run TestRealProcesses`).

* **Fuzz targets** (run each for a while, not only the seed corpus):
  `go test ./security/wire -run xxx -fuzz FuzzVerifyClear`, and likewise `FuzzBootstrapDocument`,
  `FuzzActivationStateLoad`, `FuzzJournalRecovery`, `FuzzIdentifiers`, `FuzzProtectedRelativePath`,
  `FuzzSignedIngressHandler`, `FuzzParseTarget`, `FuzzStrictJSONAndCanonicalization`.
* **Crash campaigns** kill a child process at random instants and check what is left:
  `go test ./security/activation ./security/replay ./security/secadmin -run CrashCampaign`.
* **Benchmarks**: `go test ./security/... ./junction -run xxx -bench .`
* **Wrapper protocol vectors** are shared with the wrapper libraries:
  `templates/instance/wrapper/vectors/`.

## Keeping SECURITY.md true

The capacity matrix in SECURITY.md is generated from the code. If a class or an extractor
changes, `go test .` fails; regenerate with

```sh
go test . -run SecurityDocCapacityMatrix -update-security-doc
```

and read the diff: it is the change in what the engine allows.
