# Quickbench

Quickbench runs YAML LLM benchmarks through RocketCode. Benchmark files describe behavior only; models are selected at runtime with repeatable `--model` flags.

## Configuration

Quickbench loads provider configuration from `./quickbench.json` in the current working directory.

From the repository root, start from the combined example config:

```sh
cp cmd/quickbench/quickbench.json.example quickbench.json
```

The example file includes every supported provider shape:

```json
{
  "providers": {
    "openai": {
      "apiKey": "{{ env.OPENAI_API_KEY }}",
      "baseURL": ""
    },
    "anthropic": {
      "apiKey": "{{ env.ANTHROPIC_API_KEY }}",
      "baseURL": ""
    }
  }
}
```

Only providers selected by `--model` need their referenced environment variables.

## OpenAI

Run the included example benchmark with OpenAI:

```sh
export OPENAI_API_KEY=sk-...
go run ./cmd/quickbench --model 'openai/gpt-5.5?reasoningEffort=high&verbosity=low' cmd/quickbench/examples
```

## Anthropic

Run the included example benchmark with Anthropic:

```sh
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/quickbench --model 'anthropic/claude-sonnet-4-20250514' cmd/quickbench/examples
```

## Compare Providers

Compare both providers in one run:

```sh
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/quickbench \
  --model 'openai/gpt-5.5?reasoningEffort=high&verbosity=low' \
  --model 'anthropic/claude-sonnet-4-20250514' \
  cmd/quickbench/examples
```

## JSON Output

Use `--json` for machine-readable output:

```sh
go run ./cmd/quickbench --json --model 'openai/gpt-5.5' cmd/quickbench/examples
```

## Benchmark Files

Quickbench recursively scans the directory argument for `.yaml` and `.yml` files.

Use `cmd/quickbench/examples/enum-route.yaml` as a runnable example. Use `cmd/quickbench/skills/quickbench-benchmarks` for Agent Skills-compatible instructions on writing new benchmark files.
