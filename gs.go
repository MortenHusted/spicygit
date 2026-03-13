package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// JSON types from `gs log short --json --all`

type gsBranch struct {
	Name    string    `json:"name"`
	Current bool      `json:"current"`
	Down    *gsDown   `json:"down"`
	Ups     []gsUp    `json:"ups"`
	Change  *gsChange `json:"change"`
	Push    *gsPush   `json:"push"`
}

type gsDown struct {
	Name         string `json:"name"`
	NeedsRestack bool   `json:"needsRestack"`
}

type gsUp struct {
	Name string `json:"name"`
}

type gsChange struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

type gsPush struct {
	Ahead     int  `json:"ahead"`
	Behind    int  `json:"behind"`
	NeedsPush bool `json:"needsPush"`
}

func loadBranches() ([]*gsBranch, error) {
	cmd := exec.Command("git-spice", "log", "short", "--json", "--all", "--no-prompt")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("gs log short: %w", err)
	}

	var branches []*gsBranch
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var b gsBranch
		if err := json.Unmarshal(line, &b); err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		branches = append(branches, &b)
	}
	return branches, sc.Err()
}

// repoName returns the basename of the current git repo root.
func repoName() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// isGitRepo checks whether the current directory is inside a git repository.
func isGitRepo() bool {
	err := exec.Command("git", "rev-parse", "--git-dir").Run()
	return err == nil
}

// hasCommits reports whether HEAD points to a valid commit.
func hasCommits() bool {
	return exec.Command("git", "rev-parse", "HEAD").Run() == nil
}

// ensureTrunk creates an initial empty commit on trunk if it doesn't exist yet.
// git-spice requires trunk to have at least one commit. Returns true if trunk
// is ready (already existed or was just created).
func ensureTrunk() bool {
	trunk := gsTrunkName()
	if trunk == "" {
		trunk = "main"
	}
	// Already has commits — nothing to do
	if exec.Command("git", "rev-parse", "--verify", trunk).Run() == nil {
		return true
	}
	// Create an empty commit on trunk using plumbing (no branch switch needed)
	treeOut, err := exec.Command("git", "write-tree").Output()
	if err != nil {
		return false
	}
	tree := strings.TrimSpace(string(treeOut))
	commitCmd := exec.Command("git", "commit-tree", tree, "-m", "initial commit")
	commitOut, err := commitCmd.Output()
	if err != nil {
		return false
	}
	sha := strings.TrimSpace(string(commitOut))
	return exec.Command("git", "update-ref", "refs/heads/"+trunk, sha).Run() == nil
}

// gsTrunkName reads the trunk branch name from git-spice's data store.
// Returns "" if git-spice isn't initialized or data can't be read.
func gsTrunkName() string {
	out, err := exec.Command("git", "show", "refs/spice/data:repo").Output()
	if err != nil {
		return ""
	}
	var repo struct {
		Trunk string `json:"trunk"`
	}
	if json.Unmarshal(out, &repo) != nil {
		return ""
	}
	return repo.Trunk
}

// isNotInitedError reports whether the error indicates gs hasn't been
// initialized for this repo yet (so we can prompt the user to run init).
func isNotInitedError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not initialized") ||
		strings.Contains(msg, "auto-initialize") ||
		strings.Contains(msg, "not allowed to prompt")
}

// gsExecCmd returns a bare exec.Cmd for use with tea.ExecProcess.
// Do NOT set Stdin/Stdout/Stderr — tea.ExecProcess does that.
func gsExecCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git-spice", args...)
	cmd.Env = append(cmd.Environ(), "GIT_SPICE_NO_GS_WARNING=1")
	return cmd
}

// gsBgCmd returns an exec.Cmd for background (non-interactive) execution.
func gsBgCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git-spice", args...)
	cmd.Env = append(cmd.Environ(), "GIT_SPICE_NO_GS_WARNING=1")
	return cmd
}

// currentBranch returns the name of the currently checked-out branch.
func currentBranch() string {
	out, err := exec.Command("git", "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectBranchBases finds the likely base branch for each given branch by
// walking its commit history and looking for commits that are tips of other
// branches. Returns map[branch]baseBranch. Runs up to 10 git commands
// concurrently for speed.
func detectBranchBases(branches []string) map[string]string {
	// Get all branch tips in one call.
	out, err := exec.Command("git", "for-each-ref",
		"--format=%(objectname) %(refname:short)",
		"refs/heads/",
	).Output()
	if err != nil {
		return nil
	}

	tipsByHash := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(parts) != 2 {
			continue
		}
		tipsByHash[parts[0]] = append(tipsByHash[parts[0]], parts[1])
	}

	type result struct {
		branch string
		base   string
	}

	results := make(chan result, len(branches))
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for _, branch := range branches {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			revs, err := exec.Command("git", "rev-list", b, "--max-count=100").Output()
			if err != nil {
				return
			}
			for i, sha := range strings.Split(strings.TrimSpace(string(revs)), "\n") {
				if i == 0 {
					continue // skip own tip
				}
				sha = strings.TrimSpace(sha)
				if names, ok := tipsByHash[sha]; ok {
					for _, n := range names {
						if n != b {
							results <- result{b, n}
							return
						}
					}
				}
			}
		}(branch)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	bases := map[string]string{}
	for r := range results {
		bases[r.branch] = r.base
	}
	return bases
}

// ---------- working tree / staging ----------

type fileStatus struct {
	Index    byte   // X: staged status (M, A, D, R, ?)
	WorkTree byte   // Y: unstaged status (M, D, ?)
	Path     string
}

func (f fileStatus) isUntracked() bool { return f.Index == '?' && f.WorkTree == '?' }
func (f fileStatus) isStaged() bool    { return f.Index != ' ' && f.Index != '?' }
func (f fileStatus) isUnstaged() bool  { return f.WorkTree != ' ' && f.WorkTree != 0 }

func loadFileStatuses() []fileStatus {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var files []fileStatus
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		files = append(files, fileStatus{
			Index:    line[0],
			WorkTree: line[1],
			Path:     line[3:],
		})
	}
	return files
}

func loadFileDiff(path string, staged bool) string {
	var cmd *exec.Cmd
	if staged {
		cmd = exec.Command("git", "diff", "--cached", "--", path)
	} else {
		cmd = exec.Command("git", "diff", "--", path)
	}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		// Untracked file — show content as additions
		content, err := exec.Command("git", "diff", "--no-index", "--", "/dev/null", path).Output()
		if err != nil && len(content) > 0 {
			return string(content)
		}
		// Last resort: just read the file
		raw, _ := exec.Command("cat", path).Output()
		return string(raw)
	}
	return string(out)
}

func stageFile(path string) error {
	return exec.Command("git", "add", "--", path).Run()
}

func unstageFile(path string) error {
	return exec.Command("git", "reset", "HEAD", "--", path).Run()
}

func stageAll() error {
	return exec.Command("git", "add", "-A").Run()
}

// ---------- commits ----------

type commit struct {
	Hash    string
	Subject string
	Time    string
}

// loadCommits returns commits for a branch. If base is non-empty, only
// commits in base..branch are returned (i.e. the stacked commits).
func loadCommits(branch, base string, limit int) ([]commit, error) {
	var rangeSpec string
	if base != "" {
		rangeSpec = base + ".." + branch
	} else {
		rangeSpec = branch
	}
	cmd := exec.Command("git", "log", rangeSpec,
		fmt.Sprintf("--max-count=%d", limit),
		"--format=%h\x1f%s\x1f%cr",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var commits []commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		commits = append(commits, commit{
			Hash:    parts[0],
			Subject: parts[1],
			Time:    parts[2],
		})
	}
	return commits, nil
}
