# envmask

Masks values in `.env` files so you can share them — in an issue, a chat, a code review or a support ticket — without exposing secrets. The structure (keys, empty values, comments) stays intact, so it's still clear which variables are set and which are missing. **The source file is never modified**; the result is written next to it.

## Installation

```sh
# from source
go build -o envmask .
# or a prebuilt binary
chmod +x envmask-darwin-arm64 && mv envmask-darwin-arm64 ~/.local/bin/envmask
```

If macOS blocks a downloaded binary: `xattr -d com.apple.quarantine envmask`.

## Usage

```sh
envmask .env.production              # → .env.production.masked
envmask -t .env.production           # show value types
envmask -t -o - .env | pbcopy        # straight to the clipboard (macOS)
envmask -f .env                      # overwrite an existing .masked file
envmask -strip-comments .env         # drop comments
```

## What it does

| Source | Default | With `-t` |
|---|---|---|
| `DATABASE_URL="postgres://u:p@db/app"` | `DATABASE_URL="***"` | `DATABASE_URL="<url:postgres+creds>"` |
| `SMTP_PASSWORD=` | `SMTP_PASSWORD=` | `SMTP_PASSWORD=` |
| `STRIPE_KEY=your_key_here` | `STRIPE_KEY=***` | `STRIPE_KEY=<placeholder>` |
| `PORT=3000` | `PORT=***` | `PORT=<int>` |
| `BASE_URL=${HOST}` | `BASE_URL=***` | `BASE_URL=<ref>` |
| `# OLD_TOKEN=ghp_...` | `# OLD_TOKEN=***` | `# OLD_TOKEN=***` |

- Empty values stay empty, so unset variables remain visible.
- Keys, `export`, quotes, ordering and regular comments are preserved.
- Multi-line quoted values (PEM keys) are collapsed into one.
- Commented-out assignments are masked too.
- Unrecognized lines are hidden entirely and replaced with a marker.
- **Self-check:** if any fragment of an original value (≥ 8 characters) ends up in the output — e.g. via an inline comment — the file is not written.

Types shown with `-t`: `bool`, `int`, `float`, `duration`, `url:<scheme>[+creds]`, `email`, `pem`, `json`, `list:N`, `string`, `placeholder`, `ref`. Hosts, lengths and contents of values are never revealed.

> Add `*.masked` to your global gitignore in case the file ends up inside a repository.
