# spicygit

A terminal UI for [git-spice](https://github.com/abhinav/git-spice) — manage stacked branches and PRs without leaving the terminal.

Heavily inspired by [lazygit](https://github.com/jesseduffield/lazygit). Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

> **Work in progress.** Expect rough edges. Contributions welcome.

## What is this?

git-spice is excellent for managing stacked branches and PRs, but its CLI workflow requires bouncing between commands. spicygit wraps it in an interactive TUI so you can:

- **See your stack** — tree view of all tracked and untracked branches
- **Stage, commit, amend** — without leaving the TUI, with auto-restack
- **Split branches** — visually mark commits and split into stacked branches
- **Submit PRs** — inline title editing, draft toggle, single branch or full stack
- **Navigate and search** — contextual filtering across files, branches, commits, and diffs

## Requirements

- [Go](https://go.dev/dl/) 1.24+
- [git-spice](https://github.com/abhinav/git-spice) installed and in `$PATH`
- Git 2.x

## Install

### Homebrew

```sh
brew install MortenHusted/tap/spicygit
```

### From source

```sh
go install github.com/MortenHusted/spicygit@latest
```

Or clone and build:

```sh
git clone https://github.com/MortenHusted/spicygit.git
cd spicygit
go build -o spicygit .
```

## Usage

```sh
cd your-git-repo
spicygit
```

If the repo isn't initialized with git-spice yet, spicygit will prompt you.

## Keybindings

### Navigation

| Key | Action |
|-----|--------|
| `up` / `down` | Navigate items in current panel |
| `left` / `right` | Switch panel in column (Changes / Stack) |
| `tab` | Switch between left and right columns |
| `1` / `2` / `3` | Jump to Changes / Stack / Right panel |
| `/` | Search / filter (contextual) |
| `?` | Help overlay |
| `q` | Quit |

### Changes Panel

| Key | Action |
|-----|--------|
| `space` | Stage / unstage file |
| `a` | Stage all |
| `c` | Commit (inline message, uses `gs commit create`) |
| `A` | Amend (inline message, uses `gs commit amend`) |
| `D` | Discard file changes |

### Stack Panel

| Key | Action |
|-----|--------|
| `space` | Checkout branch |
| `n` | New branch (stacked above current) |
| `R` | Rename branch (inline) / Repo restack (on trunk) |
| `d` | Delete branch |
| `r` | Restack branch |
| `s` | Submit PR (inline title, `ctrl+d` for draft) |
| `S` | Submit entire stack |
| `e` | Edit commits (interactive rebase) |
| `b` | Split branch on commits |
| `O` | Move onto different base |
| `I` | Insert branch mid-stack |
| `f` | Fold into parent |
| `Q` | Squash into one commit |
| `t` | Track with git-spice |
| `T` | Untrack |
| `o` | Open PR in browser |
| `y` | Repo sync (on trunk) |

## How it works

spicygit calls `git-spice` and `git` as external commands — it doesn't import or link against git-spice code. All git-spice operations go through the `git-spice` CLI binary.

## License

MIT

## Acknowledgments

- [git-spice](https://github.com/abhinav/git-spice) by Abhinav Gupta — the stacked branch engine
- [lazygit](https://github.com/jesseduffield/lazygit) by Jesse Duffield — UX inspiration
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lipgloss](https://github.com/charmbracelet/lipgloss) by Charm — TUI framework
