# Zamnius

Multi-Agent Framework on Go

## Examples

Runnable workflow definitions live in [`examples/workflows/`](examples/workflows/): one workflow
written three ways (`hello.yaml`, `hello.json`, `hello.toml`), a three-branch fan-out with an `all`
join (`fanout.yaml`), a budget-bounded run (`budget-tokens.yaml`), a two-branch run for interruption
and resume drills (`slow.yaml`), and a deliberately invalid definition (`invalid.yaml`).

```bash
go run ./cmd/zamnius run start --file examples/workflows/hello.yaml
```

## Version


