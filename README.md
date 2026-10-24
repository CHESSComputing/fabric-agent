# FabricNode AI Agent for FOXDEN Infrastructure

An interactive agent written in Go that lets users talk to a CHESS
[FabricNode](https://github.com/CHESSComputing/FabricNode) in plain English.
A question is turned into calls against the node's REST API, the answer is shown
as a table (and optionally as a short prose summary), and the matching datasets
can then be downloaded and processed.

## What the node can and cannot do

The data-service endpoint named `/sparql` is **not a SPARQL engine**. It is a
triple-pattern API, and the agent plans against what it really offers:

| node API                                        | meaning                                              |
|-------------------------------------------------|------------------------------------------------------|
| `GET /graphs`, `/beamlines/{bl}/graphs`         | dataset graphs (one per dataset)                     |
| `GET /sparql?s=&p=&o=&g=`                       | exact-match triple filter, at most 100 triples       |
| `GET /sparql?search=<kw>`                       | substring search in **literal values only**          |
| `GET /sparql?describe=<iri>`                    | triples where the IRI is subject or object           |
| `GET /beamlines/{bl}/datasets/{did}/sparql`     | all triples of one dataset (used for downloads)      |
| `GET /catalog/beamlines` (catalog service)      | beamline catalog                                     |

Sample names (e.g. `thaumatin-a`) live in the graph IRI
`<base>/graph/<beamline>/btr=…/cycle=…/sample_name=…`, which `search` cannot
see. The agent's `find` operation therefore combines graph-name matching with
the node's literal search.

## How it works

```
question ─► schema ─► LLM planner ─► validate ─► execute calls ─► tables / summary
(English)   beamlines  JSON plan:     op, args    node REST API         │
            graphs     intent + 1-3   checked     (see table above)     ▼
            predicates calls                          ▲          download / process
                          ▲                           │          (asks before writing)
                          └── node error / nothing found fed back (max_retries)
```

Operations the planner can choose (`pkg/agent/calls.go`):
`find` (keywords in graph name or text values, ANDed), `list_graphs`
(optionally by beamline and name keywords), `triples` (exact pattern),
`describe`, `beamlines`.

Intent `download` saves the matching datasets; `process` also derives a new
dataset and registers it with the Notification Service, but only after you
confirm (or pass `--yes`). The last three questions are remembered for
follow-ups.

## Configuration and debugging

See `config.yaml`. Key settings: `agent.mode` (`ollama`, `openai`, `anthropic`,
`heuristic`), `agent.model`, `agent.num_ctx` (Ollama context window) and
`log_level`.

`log_level` (also `--log-level` or `FABRIC_AGENT_LOG_LEVEL`) controls what is
written to stderr:

| level   | shows                                                                           |
|---------|---------------------------------------------------------------------------------|
| `off`   | nothing                                                                         |
| `info`  | every node API call (URL, status, size, time), every LLM call, the plan, retries, "still waiting" heartbeats |
| `debug` | + node response bodies, the system/user prompts, the raw LLM reply, schema contents (truncated) |
| `trace` | + everything untruncated, including the HTTP bodies sent to the LLM             |

```bash
./bin/fabric-agent --log-level debug --query "Which datasets mention thaumatin?"
```

## Troubleshooting Ollama

The agent checks Ollama before its first request and logs (level `info`) whether
it is reachable, which models are installed and loaded, and whether the model
runs on CPU or GPU. To check by hand:

```bash
curl http://localhost:11434/                  # -> "Ollama is running"
curl http://localhost:11434/api/tags          # installed models (is "mistral" listed?)
ollama ps                                     # loaded models; PROCESSOR column shows CPU vs GPU
time curl http://localhost:11434/api/chat -d \
  '{"model":"mistral","messages":[{"role":"user","content":"Say hi"}],"stream":false}'
```

A browser visiting `/api/chat` shows `405 Method Not Allowed`: that is normal,
the endpoint only accepts POST, and it proves the server is up.

If the last command is slow, the model is simply slow on your hardware: the
first request loads the model into memory, then the prompt (~1,200 tokens) is
processed, then the reply is generated. Remedies: warm the model once with
`ollama run mistral "hi"`, use a smaller model (e.g. `llama3.2:3b` or
`qwen2.5:3b`), lower `agent.num_ctx` to 4096, use a GPU, and raise
`agent.timeout_seconds` if needed. The `info` log prints a "still waiting"
line every 10 seconds, and Ollama's token counts when it answers.

## Usage

```bash
make build
make run                                   # interactive
./bin/fabric-agent --query "Which datasets mention thaumatin?"
./bin/fabric-agent --query "Download the silicon standard from beamline ID1"
./bin/fabric-agent --query "Process all thaumatin datasets" --yes
```

## Development

```bash
make vet
make test
```

Tests use an in-process fake node (`internal/fakenode`) and a scripted LLM, so
they need no network, node or API key.
