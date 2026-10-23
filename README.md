# FabricNode AI Agent for FOXDEN Infrastructure

An interactive agent written in Go that lets users talk to a CHESS
[FabricNode](https://github.com/CHESSComputing/FabricNode) in plain English.
A question is translated into a SPARQL query, executed against the node,
repaired if the endpoint rejects it, and the answer is shown as a table (and
optionally as a short prose summary). Matching datasets can then be downloaded
and processed.

## How it works

```
 question ─► schema discovery ─► LLM planner ─► validator ─► SPARQL endpoint ─► table / summary
 (English)   catalog /.well-known   JSON plan     read-only     data-service        │
             VoID, SHACL, examples  intent+SPARQL LIMIT cap     /sparql             ▼
             /graphs, live classes       ▲                          │        download / process
             and properties              └── error or empty result ◄┘        (asks before writing)
                                             fed back (max_retries)
```

1. **Schema discovery** (`pkg/agent/schema.go`). Nothing about beamlines,
   samples or vocabularies is hardcoded. On first use the agent reads the
   node's self-description (`/.well-known/void`, `/shacl`, `/sparql-examples`
   from the Catalog Service), the list of named graphs (`/graphs`) and the
   classes and properties actually in use, and caches them
   (`schema_ttl_seconds`). This follows the node's "progressive disclosure"
   design.
2. **Planning** (`pkg/agent`). The LLM returns JSON with an `intent`
   (`query`, `download`, `process`), the `sparql`, and a one-line explanation,
   or a `clarification` question if the request is ambiguous.
3. **Validation** (`pkg/sparql`). LLM output is untrusted. Only
   SELECT/ASK/CONSTRUCT/DESCRIBE pass; updates and `SERVICE` are rejected; a
   top-level `LIMIT` (at most `max_rows`) is enforced.
4. **Execution and repair.** If the endpoint returns an error, or the query
   returns nothing, the message is sent back to the LLM for another attempt.
5. **Follow-up actions** (`pkg/workflow`). `download` saves the matching graphs;
   `process` also derives a new dataset and registers it through the
   Notification Service, but only after you confirm (or pass `--yes`).

The last three questions are remembered, so follow-ups such as
"now only the ones measured on ID1" work in the interactive prompt.

## Configuration

See `config.yaml`. The important part is `agent.mode`:

| mode        | what it uses                                           |
|-------------|--------------------------------------------------------|
| `openai`    | OpenAI, or any OpenAI-compatible server via `base_url` |
| `anthropic` | Anthropic Messages API                                 |
| `ollama`    | a local Ollama server; data never leaves your machine  |
| `heuristic` | no LLM; keyword search only (`mock`/`live` are aliases)|

API keys can be given in the file or via `OPENAI_API_KEY` /
`ANTHROPIC_API_KEY`; the FabricNode token via `FABRIC_AUTH_TOKEN`.

If your SPARQL endpoint supports only a subset of SPARQL, tell the model with
`agent.dialect_hint`. Errors are also fed back automatically.

## Usage

```bash
make build
make run                                   # interactive
./bin/fabric-agent --query "Which datasets mention thaumatin?"
./bin/fabric-agent --query "Download the silicon standard on ID1" 
./bin/fabric-agent --query "Process all FAST nanoparticle samples" --yes
```

Every answer prints the generated SPARQL so you can check what was asked.

## Development

```bash
make vet
make test
```

Tests use a fake FabricNode (`httptest`) and a scripted LLM, so they need no
network or API key.
