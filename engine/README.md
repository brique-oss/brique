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
| `cmd/brique-sec/` | offline administration of the security directory |
| `shared/` | files, atomic replace, protected files (unix and windows) |
| `template/` | the canonical `context.json` and capacity templates |
| `sandbox/` | fixtures used by the integration tests |

## Build and run

```sh
go build ./...                      # the engine and brique-sec
go build -o brique-engine .
go build -o brique-sec ./cmd/brique-sec

./brique-engine -root_path <instance>                         # local only: no external boundary
./brique-engine -root_path <instance> -security_dir <dir> -security_generation g000001   # first start with signed communication
./brique-engine -root_path <instance> -security_dir <dir>                                # later starts recover the durable generation
```

Without `-security_dir` the engine is local-only. Flags: `-root_path`, `-security_dir`,
`-security_generation` (first activation only), `-audit_max_file_mib` (default 64),
`-audit_retention_days` (default 14). Reload a new policy generation with `brique-sec apply
--reload` (SIGHUP on unix, a request file on windows).

Platforms: linux, darwin and windows. The windows code is cross-compiled and vetted
(`GOOS=windows go vet ./...`); it has not yet been run on a windows machine.

## Test

```sh
go test ./...                       # everything; the integration tests start real engines
go test -race ./...
GOOS=windows go vet ./...           # type-checks the windows code and its tests
```

Some tests need `python3` and a C++ compiler (`c++`, with libcurl) and skip if they are absent.

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
