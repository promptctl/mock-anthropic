# mock-anthropic

A local server that stands in for every host Claude Code talks to, so a program that runs Claude Code can be tested with no login, no subscription, no network, and the same result every run.

It answers by generating responses from the request, or by replaying recordings of real conversations that you keep in your own repository. It never launches Claude Code: you point any Claude Code instance at it. It is not tied to a Claude Code version; recordings made on one version replay on others.

## Install

Pin a release tag:

```sh
go install github.com/promptctl/mock-anthropic@v0.1.0
```

The binary is `mock-anthropic`. It needs `openssl` on the path the first time it starts, to generate its certificate authority.

## Quick start

Start the mock, log a Claude Code config directory in, and run Claude Code against it:

```sh
mock-anthropic serve --port 0 --scenarios ./scripts 2>serve.log | tee serve.json &
# serve.json: {"port": 51234, "ca": "/path/to/ca/cert.pem"}

mock-anthropic login /tmp/cc-config --trust "$PWD"

eval "$(mock-anthropic env --port 51234 --ca /path/to/ca/cert.pem --config-dir /tmp/cc-config | sed 's/^/export /')"
claude -p "Say hello and nothing else." --output-format text
```

With no script matching, the mock answers a Messages request with a 400 and an `UNMATCHED` line on stderr. Write a script (below) or replay a recording.

Claude Code reaches the mock as an HTTPS proxy and trusts its CA through `NODE_EXTRA_CA_CERTS`. `env` prints exactly the variables that do this. The variables that would route around the mock or replace its login must be unset in the instance under test; `PROJECT.md` lists them under `env`.

## Commands

`mock-anthropic <command> [args]`. Exit codes: 0 success, 1 the answer is "no" or the mock failed, 2 a wrong command line. Every invocation ends with one line on stderr, beginning `OK`, `NO`, `USAGE` or `FAILED`, followed by a JSON record of what it did.

| Command | What it does |
| --- | --- |
| `serve [--port N] [--scenarios DIR]... [--recordings DIR]... [--account FILE]` | Start the mock. Port 0 picks a free port. Once listening it prints one JSON line on stdout, `{"port": N, "ca": "<path>"}`, and logs on stderr. It runs until SIGINT, SIGTERM or SIGHUP. |
| `login <config-dir> [--trust <project-dir>]... [--account FILE]` | Write a Claude Code config directory that the mock accepts as logged in, with the given projects trusted. Set `CLAUDE_CONFIG_DIR` to exactly the path you passed. |
| `env --port N --ca FILE --config-dir DIR` | Print the `NAME=value` environment that points a Claude Code instance at a running mock. |
| `record --dir DIR [--port N] [--account FILE]` | Like `serve`, but forward each request to the real host with the client's own login, and save the Messages exchanges. |
| `coverage` | List every known endpoint as answered or out of scope. Exits 1 if an in-scope endpoint has no generator. |

`--account FILE` replaces the default account (a synthetic one) that the mock logs Claude Code in as. Use the same file for `login`, `serve` and `record`.

## Reading the log

Every request and every connection writes exactly one line to stderr, starting with its outcome: `SERVED`, `UNMATCHED`, `FAILED` or `ABORTED` for a request; `CLOSED`, `REFUSED` or `FAILED` for a connection. A test should fail on any `UNMATCHED`, `REFUSED` or `FAILED` line. An `UNMATCHED` line names the method, host and path, and lists every script it tried.

## Scripts

A script is a JSON file in a `--scenarios` directory that says which Messages requests it answers and what to reply. Directories are read as each request is answered, so a test can write a script after the mock has started. Write it under another name and rename it into place so the mock never reads it half-written: only names ending `.json` that do not start with `.` are scripts.

```json
{
  "match": {
    "bodyContains": ["list the files"],
    "lastUserContains": ["list the files"]
  },
  "reply": {
    "kind": "message",
    "content": [
      {"type": "text", "text": "Listing them now."},
      {"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}
    ],
    "stop_reason": "tool_use",
    "eventDelayMs": 0
  }
}
```

- `match` is optional. Every `bodyContains` string must appear in the raw request body; every `lastUserContains` string must appear in the last `user` message's text. With no conditions, the script answers every Messages request.
- `reply.kind` is `message` or `literal`. A message holds `text`, `tool_use` and `thinking` blocks and a `stop_reason` of `end_turn`, `tool_use` or `max_tokens`; `tool_use` goes with a `tool_use` block and `end_turn` with none. The mock streams it as server-sent events if the request asks for a stream, and returns JSON otherwise. `eventDelayMs` waits between streamed events.
- A `literal` reply is a status (200 to 599), optional `headers` and a `body` string, sent as written. Use it to test error handling: a 429, a 500, an overloaded error.
- Scripts are tried in file-name order within a directory, directories in the order given, then recordings, then the mock's built-in answers. The first match answers.
- A script that cannot be read fails every Messages request, matched or not, with a 500 and a `FAILED` line naming its path.

The mock also answers Claude Code's own side calls without a script: session titles, the auto-mode permission classifier (allows by default), and the quota probe. A script that matches one of them wins.

## Recording and replaying

Recordings let you capture a real conversation once and replay it offline, on any Claude Code version.

**Record.** With your real Claude Code login, point the instance at `record` and run the conversation:

```sh
mkdir recordings
mock-anthropic record --dir recordings --port 18801 &
HTTPS_PROXY=http://127.0.0.1:18801 NODE_EXTRA_CA_CERTS=<ca path from the startup line> \
  claude -p "Run the shell command: echo hello. Then reply with the single word done." --allowedTools Bash
# SIGINT the mock when the run ends
```

Each successful Messages exchange is scrubbed and saved as the next `NNNN.json`. Credentials, email addresses, the account UUID and every other UUID are replaced before saving, and an exchange with any left is not saved. Other text (file paths, host names, your prompt, the system prompt) is saved as it is: read a recording before you commit it.

**Replay.** Log in with `login`, then serve the directory:

```sh
mock-anthropic serve --recordings recordings
```

A request is matched to a recording by its place in the conversation, the text of its prompt, and its tool results. The system prompt, model, session IDs and the rest are not compared, which is why a recording made on one Claude Code version answers another. Edit the prompt, and the request is `UNMATCHED`. Replay must run with the same `--account` the recording was made with.

Recordings belong in the repository that uses them; this repository holds none. The startup record names any recordings shadowed by an earlier one with the same key.

## Endpoints

`mock-anthropic coverage` is the list of what is answered. Out-of-scope endpoints (remote sessions, the browser login flow, feedback upload) are refused with a 400 that names the reason. A `CONNECT` to a host the mock does not know is refused with a 403.

## Releases

Releases are git tags; pin one. `PROJECT.md` is the specification.
