# spicygit

Terminal UI for [git-spice](https://github.com/abhinav/git-spice) — manage stacked branches and PRs without leaving the terminal.

## Tech Stack

- Go 1.24
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) v1 (Elm-architecture TUI framework)
- [Lipgloss](https://github.com/charmbracelet/lipgloss) v1 (terminal styling)
- [charmbracelet/x/ansi](https://github.com/charmbracelet/x) (ANSI-aware string ops for overlay compositing)
- Wraps `git-spice` CLI via `exec.Command` — no library imports from git-spice

## Project Structure

- `main.go` — entry point, just runs Bubble Tea
- `tui.go` — all TUI logic: model, update, view, panels, input modes, rendering (~2600 lines)
- `gs.go` — git and git-spice interaction: command wrappers, branch loading, file status, commit loading, tree building

## Architecture

Single `model` struct following Bubble Tea's Elm architecture (Model → Update → View).

### Panels
Three independent bordered panels in two columns:
- **Left column**: Changes (top), Stack (bottom)
- **Right column**: contextual Diff or Commits (full height)

Panel enum: `panelChanges`, `panelStack`, `panelRight`.

### Navigation
- `up/down` — items within focused panel
- `left/right` — switch panels in same column (Changes <-> Stack)
- `tab` — switch between left and right columns
- `1/2/3` — jump to panel directly
- No vim bindings

### Key Modes
The TUI has several modal states checked in this order in `Update()`:
1. `pendingOp` — branch picker for "onto" operation
2. `splitMode` — marking commits to split a branch
3. `inputMode` — inline text input (commit message, branch name, rename, submit title, etc.)
4. `searching` — contextual search/filter
5. `showHelp` — help overlay
6. Normal key handling

### Commands
- `runGS(args...)` — interactive git-spice command, suspends TUI (used for branch create, edit)
- `runGSBg(args...)` — background git-spice command, TUI stays up (used for most operations)
- `runGit(args...)` — interactive git command with error capture
- Both `runGS` and `runGit` tee stdout+stderr via `io.MultiWriter` to capture error messages

### Auto-refresh
2-second polling via `tea.Tick` reloads file statuses. Full branch tree reloads after every operation (`opDoneMsg → doLoad`).

### Edge Cases
- `ensureTrunk()` auto-creates an empty initial commit on trunk via git plumbing when trunk has no commits (allows git-spice to work in fresh repos)
- `gsCanCommit()` is not used — git-spice handles fresh repos natively with auto-init

## Build & Run

```
go build -o spicygit .
./spicygit
```

Requires `git-spice` to be installed and available in `$PATH`.

## Conventions

- Keybindings follow lazygit where applicable (space=checkout/stage, n=new, d=delete, R=rename, c=commit, A=amend)
- git-spice specific operations keep their own keys (s=submit, S=stack submit, r=restack, b=split, etc.)
- Inline input preferred over editor/TUI suspension for high-frequency operations
- TUI suspension (`runGS`) only for complex interactive operations (branch create with staged changes, edit/rebase)
- Status bar shows context-sensitive hints and last executed command
