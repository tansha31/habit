# habit

A premium, keyboard-first habit tracker for the macOS terminal. One binary,
three faces: a Bubble Tea TUI, a scriptable CLI, and an optional launchd
reminder agent — all over a single SQLite file.

```
  ⬢ habit          Dashboard    Analytics    Settings         Tue · Jul 7
                   ━━━━━━━━━

   Today · 6 of 9   ▰▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱  67%                     ❄ 2

   MORNING ─────────────────────────────────────────────────────── 3/3

     ✓  Meditate           10 min          ▂▄▆█▆▄▆▂▄▆▆█▄▆     ◆ 47d
     ✓  Journal                            ▄▄▂▄▄▄▆▄▄▄▂▄▄▄       12d
```

## Build & run

```sh
go build -o habit ./cmd/habit
./habit              # TUI
./habit --help       # CLI
```

Requires Go 1.26+. Pure-Go SQLite (`modernc.org/sqlite`) — no CGO, trivially
cross-compiles for arm64 + x86_64.

## The TUI

Launch opens the dashboard focused on the current time-of-day group with the
first incomplete habit pre-selected. Two keystrokes — launch, `Space` — and
the day's next habit is logged.

| Key | Action |
|---|---|
| `Space`/`Enter` | Toggle done · log quantified target |
| `+` `-` | Nudge a quantified amount by its step |
| `s` | Skip with a reason (t/v/s/o/n) |
| `n` `e` | New habit · edit selected (type in the Group field to create a group) |
| `dd` | Archive (undoable — nothing ever asks "are you sure?") |
| `u` / `Ctrl+R` | Undo / redo, shared with the CLI |
| `/` or `Ctrl+P` | Command palette (`>` commands · `@` dates · `#` tags) |
| `1` `2` `3` | Dashboard · Analytics · Settings |
| `?` | The full keymap |

Every mutation applies instantly, persists asynchronously, and is reversible
with `u` — even after a crash, even if the mutation came from the CLI.

## The CLI

```sh
habit add "Read fiction" --quantified 20min --step 5 --group afternoon --tag reading
habit done meditate                 # ✓ meditate · streak 31
habit done read-fiction --amount 10 # accumulates through the day
habit skip gym --reason tired
habit undo
habit status --json                 # stable schema for scripts
habit export --format csv --from -30
habit doctor                        # integrity check + cache rebuild
```

Exit codes: `0` ok · `1` error · `2` unknown slug (with a fuzzy suggestion).

## Prompt integration

`habit status --prompt` prints a segment like `⬢ 6/9 ◆47` in under 10 ms.

**Starship** (`~/.config/starship.toml`):

```toml
[custom.habit]
command = "habit status --prompt"
when = "command -v habit"
format = "[$output]($style) "
```

**Powerlevel10k** (`~/.p10k.zsh`):

```zsh
function prompt_habit() { p10k segment -t "$(habit status --prompt)" }
# then add `habit` to POWERLEVEL9K_RIGHT_PROMPT_ELEMENTS
```

**Plain zsh** (`~/.zshrc`):

```zsh
precmd() { RPROMPT="$(habit status --prompt 2>/dev/null)" }
```

## Reminders (habitd)

Off by default. `habit daemon install` writes a LaunchAgent that wakes every
30 minutes, reads the database read-only, and posts one notification per
group for habits past their reminder time — never more than one per group
per day, silent during quiet hours. `habit daemon remove` uninstalls.

## Configuration

`~/.config/habit/config.toml` — themes (tokyo-night, catppuccin-mocha,
gruvbox-dark, nord, rose-pine, or drop your own in
`~/.config/habit/themes/`), accent override, borders (rounded/square/ascii),
day rollover hour (default 03:00 — late nights count as today), week start,
freeze tokens, quiet hours. The Settings tab edits the same file with a live
preview; external edits hot-reload.

## Data

One SQLite file (WAL) at `~/Library/Application Support/habit/habit.db`
(`HABIT_DB` overrides). Plain schema, honest exports, zero telemetry, no
network calls. Streaks earn freeze tokens (1 per 10 completions, cap 3) that
auto-spend to protect streaks ≥ 7 days on a missed day.
