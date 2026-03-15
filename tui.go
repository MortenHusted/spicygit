package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ---------- panels ----------

type panel int

const (
	panelChanges panel = iota
	panelStack
	panelRight
)

// ---------- styles ----------

var (
	clrAccent  = lipgloss.Color("6")
	clrDim     = lipgloss.Color("238")
	clrMidGray = lipgloss.Color("242")
	clrText    = lipgloss.Color("252")
	clrYellow  = lipgloss.Color("3")
	clrGreen   = lipgloss.Color("2")
	clrRed     = lipgloss.Color("1")
	clrMagenta = lipgloss.Color("5")
	clrSelBg   = lipgloss.Color("236")
	clrStatusBg = lipgloss.Color("235")

	stBorder       = lipgloss.NewStyle().Foreground(clrDim)
	stBorderActive = lipgloss.NewStyle().Foreground(clrAccent)
	stTitle        = lipgloss.NewStyle().Bold(true).Foreground(clrAccent)
	stTitleInact   = lipgloss.NewStyle().Foreground(clrMidGray)
	stDim          = lipgloss.NewStyle().Foreground(clrMidGray)
	stCurrent      = lipgloss.NewStyle().Bold(true).Foreground(clrAccent)
	stRestack      = lipgloss.NewStyle().Foreground(clrYellow)
	stPROpen       = lipgloss.NewStyle().Foreground(clrGreen)
	stPRMerged     = lipgloss.NewStyle().Foreground(clrMagenta)
	stPRClosed     = lipgloss.NewStyle().Foreground(clrRed)
	stErr          = lipgloss.NewStyle().Foreground(clrRed).Bold(true)
	stSel          = lipgloss.NewStyle().Background(clrSelBg)
	stKey          = lipgloss.NewStyle().Foreground(clrAccent).Bold(true)
	stDesc         = lipgloss.NewStyle().Foreground(clrMidGray)
	stUntracked    = lipgloss.NewStyle().Foreground(clrMidGray).Italic(true)
	stWarning      = lipgloss.NewStyle().Foreground(clrYellow)
	stHash         = lipgloss.NewStyle().Foreground(clrYellow)
	stTime         = lipgloss.NewStyle().Foreground(clrMidGray)
	stAppTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	stStatusBar    = lipgloss.NewStyle().Background(clrStatusBg)
)

// ---------- item ----------

type item struct {
	node          *node
	untrackedName string // flat untracked (no detected base)
	isSep         bool
}

func (it item) name() string {
	if it.node != nil {
		if it.node.isUntracked {
			return it.node.uName
		}
		return it.node.branch.Name
	}
	return it.untrackedName
}

func (it item) isUntrackedItem() bool {
	return it.untrackedName != "" || (it.node != nil && it.node.isUntracked)
}

func (it item) selectable() bool { return !it.isSep }

// ---------- key help ----------

type keyHelp struct {
	key  string
	desc string
	cmd  []string
	url  string
}

func changesKeys() []keyHelp {
	return []keyHelp{
		{key: "space", desc: "stage / unstage file"},
		{key: "a", desc: "stage all files"},
		{key: "c", desc: "commit (opens editor)"},
		{key: "A", desc: "amend last commit"},
		{key: "D", desc: "discard file changes"},
	}
}

func keysFor(it *item) []keyHelp {
	if it == nil || it.isSep {
		return nil
	}

	// Any untracked branch (flat or in-tree)
	if it.isUntrackedItem() {
		name := it.name()
		return []keyHelp{
			{key: "space", desc: "checkout (git)", cmd: []string{"--git-checkout--", name}},
			{key: "t", desc: "track with git-spice", cmd: []string{"branch", "track", name}},
			{key: "d", desc: "delete branch", cmd: []string{"branch", "delete", name}},
		}
	}

	n := it.node
	if n.isTrunk {
		return []keyHelp{
			{key: "y", desc: "repo sync — pull latest from remote", cmd: []string{"repo", "sync"}},
			{key: "R", desc: "repo restack — restack all tracked", cmd: []string{"repo", "restack"}},
		}
	}

	ks := []keyHelp{
		{key: "space", desc: "checkout", cmd: []string{"branch", "checkout", n.branch.Name}},
		{key: "n", desc: "new branch stacked above", cmd: []string{"branch", "create"}},
		{key: "R", desc: "rename branch", cmd: []string{"branch", "rename", n.branch.Name}},
		{key: "d", desc: "delete branch", cmd: []string{"branch", "delete", n.branch.Name}},
		{key: "r", desc: "restack this branch", cmd: []string{"branch", "restack", n.branch.Name}},
		{key: "s", desc: "submit — create or update PR", cmd: []string{"branch", "submit", n.branch.Name}},
		{key: "S", desc: "submit stack — all PRs", cmd: []string{"stack", "submit"}},
		{key: "e", desc: "edit commits (interactive rebase)", cmd: []string{"branch", "edit", n.branch.Name}},
		{key: "b", desc: "split branch on commits", cmd: []string{"branch", "split", "--branch", n.branch.Name}},
		{key: "I", desc: "insert — create branch and restack child onto it"},
		{key: "O", desc: "onto — move onto a different base", cmd: []string{"upstack", "onto", n.branch.Name}},
		{key: "Q", desc: "squash into one commit", cmd: []string{"branch", "squash", n.branch.Name}},
		{key: "f", desc: "fold — merge into parent", cmd: []string{"branch", "fold", "--branch", n.branch.Name}},
		{key: "t", desc: "track with git-spice", cmd: []string{"branch", "track", n.branch.Name}},
		{key: "T", desc: "untrack", cmd: []string{"branch", "untrack", n.branch.Name}},
	}
	if n.branch.Change != nil && n.branch.Change.URL != "" {
		ks = append(ks, keyHelp{key: "o", desc: "open PR in browser", url: n.branch.Change.URL})
	}
	return ks
}

// ---------- messages ----------

type loadResult struct {
	branches       []*gsBranch
	untracked      []string
	bases          map[string]string // untracked branch → detected base
	curBranch      string            // currently checked-out branch
	noCommits      bool              // repo has no commits yet
	noCommitsTrunk string            // trunk name from gs config (when noCommits)
	files          []fileStatus      // working tree changes
}

type commitsLoaded struct {
	branch  string
	commits []commit
}

// cmdResult is returned by background (non-interactive) gs/git commands.
type cmdResult struct {
	err    error
	output string
}

type opDoneMsg struct{}
type errMsg struct{ err error }
type tickRefresh struct{}

// needsRestackMsg is sent when a submit fails because a branch needs restacking.
type needsRestackMsg struct {
	branch string
	args   []string // original submit args to retry after restack
}
type noGitRepoMsg struct{}
type noGitSpiceMsg struct{}
type diffLoaded struct {
	path string
	diff string
}
type filesReloaded struct {
	files []fileStatus
}

// insertCreatedMsg is sent after the "create" step of an insert operation.
// The child branch still needs to be moved onto the newly created branch.
type insertCreatedMsg struct {
	child string // branch to move onto the new one
}

// ---------- tree node ----------

type node struct {
	branch      *gsBranch
	connector   string
	isTrunk     bool
	isUntracked bool   // detected (not gs-tracked) branch in the tree
	uName       string // name for untracked nodes
	uIsCurrent  bool   // checked-out branch (for untracked nodes)
}

// treeEntry is an intermediate tree structure used to build the unified
// tracked + detected-untracked tree before flattening to nodes.
type treeEntry struct {
	name     string
	branch   *gsBranch // nil for untracked
	isTrunk  bool
	children []*treeEntry
}

func buildUnifiedTree(branches []*gsBranch, bases map[string]string) *treeEntry {
	trackedSet := make(map[string]*gsBranch, len(branches))
	for _, b := range branches {
		trackedSet[b.Name] = b
	}

	// Find trunk
	var trunk *gsBranch
	for _, b := range branches {
		if b.Down == nil {
			trunk = b
			break
		}
	}
	if trunk == nil {
		return nil
	}

	// Build children lookup: parent name → children names (from detected bases)
	detectedChildren := map[string][]string{}
	for branch, base := range bases {
		if _, isTracked := trackedSet[branch]; !isTracked {
			detectedChildren[base] = append(detectedChildren[base], branch)
		}
	}

	// Sort detected children alphabetically for stable output
	for k := range detectedChildren {
		names := detectedChildren[k]
		sortStrings(names)
		detectedChildren[k] = names
	}

	var build func(name string, gb *gsBranch, isTrunk bool) *treeEntry
	build = func(name string, gb *gsBranch, isTrunk bool) *treeEntry {
		e := &treeEntry{name: name, branch: gb, isTrunk: isTrunk}

		// Tracked children (from gs Ups)
		if gb != nil {
			for _, up := range gb.Ups {
				if child := trackedSet[up.Name]; child != nil {
					e.children = append(e.children, build(up.Name, child, false))
				}
			}
		}

		// Detected untracked children
		for _, childName := range detectedChildren[name] {
			e.children = append(e.children, build(childName, nil, false))
		}

		return e
	}

	return build(trunk.Name, trunk, true)
}

func flattenTree(tree *treeEntry, curBranch string) []node {
	var result []node
	var walk func(e *treeEntry, prefix string, isLast, isTrunk bool)
	walk = func(e *treeEntry, prefix string, isLast, isTrunk bool) {
		var connector string
		if !isTrunk {
			if isLast {
				connector = prefix + "└─ "
			} else {
				connector = prefix + "├─ "
			}
		}

		n := node{connector: connector, isTrunk: isTrunk}
		if e.branch != nil {
			n.branch = e.branch
		} else {
			n.isUntracked = true
			n.uName = e.name
			n.uIsCurrent = (e.name == curBranch)
		}
		result = append(result, n)

		var childPrefix string
		switch {
		case isTrunk:
			childPrefix = ""
		case isLast:
			childPrefix = prefix + "   "
		default:
			childPrefix = prefix + "│  "
		}
		for i, child := range e.children {
			walk(child, childPrefix, i == len(e.children)-1, false)
		}
	}
	walk(tree, "", true, true)
	return result
}

// sortStrings sorts a slice of strings in place (simple insertion sort).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// ---------- model ----------

type model struct {
	items     []item
	cursor    int
	scrollTop int

	searching bool
	query     string
	showHelp  bool

	width  int
	height int
	repo   string

	err       error
	noGitRepo bool
	noCommits bool // repo has no commits — plain git mode
	notInited bool
	loading   bool

	focus       panel
	rightCtx   panel // which left panel's context the right panel shows (panelChanges or panelStack)

	// Changes section
	files          []fileStatus
	fileCursor     int
	fileScroll     int
	fileDiff       string
	fileDiffScroll int

	// Stack section
	commits        []commit
	commitCursor   int
	commitScroll   int
	selectedBranch string
	lastCmd        string
	detectedBases  map[string]string // untracked branch → detected base

	// Pending operation: user marked a branch and is picking a target inline.
	pendingOp     string // e.g. "onto" — empty means no pending op
	pendingBranch string // the branch the op applies to

	// Inline text input (for branch creation when git-spice can't prompt)
	inputMode  string // e.g. "create-branch" — empty means no input active
	inputLabel string // prompt label shown to user
	inputBuf   string // current text

	// Split mode: user is marking commits to split a branch
	splitMode      bool
	splitBranch    string
	splitMarkers   map[int]string // commit index → new branch name

	// Submit mode
	submitDraft bool // toggle draft status for submit
	submitForce bool // force submit (bypass restack check)

	// Confirm prompt (y/n)
	confirmMsg    string   // message to show
	confirmArgs   []string // args to run on confirm
	confirmBranch string   // branch for restack+retry
}

func (m model) visibleItems() []item {
	if m.query == "" || m.focus != panelStack {
		return m.items
	}
	q := strings.ToLower(m.query)
	var out []item
	for _, it := range m.items {
		if it.isSep {
			continue
		}
		if strings.Contains(strings.ToLower(it.name()), q) {
			out = append(out, it)
		}
	}
	return out
}

func (m model) visibleFiles() []fileStatus {
	if m.query == "" || m.focus != panelChanges {
		return m.files
	}
	q := strings.ToLower(m.query)
	var out []fileStatus
	for _, f := range m.files {
		if strings.Contains(strings.ToLower(f.Path), q) {
			out = append(out, f)
		}
	}
	return out
}

func (m model) visibleCommits() []commit {
	if m.query == "" || m.focus != panelRight || m.rightShowsDiff() {
		return m.commits
	}
	q := strings.ToLower(m.query)
	var out []commit
	for _, c := range m.commits {
		if strings.Contains(strings.ToLower(c.Subject), q) ||
			strings.Contains(c.Hash, q) {
			out = append(out, c)
		}
	}
	return out
}

func (m model) sel() *item {
	vis := m.visibleItems()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return nil
	}
	it := vis[m.cursor]
	return &it
}

func (m model) selTracked() *node {
	it := m.sel()
	if it == nil || it.node == nil {
		return nil
	}
	return it.node
}

// panelWidths returns the inner content width for left and right panels.
func (m model) panelWidths() (int, int) {
	// Layout: │sp[left]sp│ sp │sp[right]sp│ = leftW+4 + 1 + rightW+4 = w
	total := m.width - 9
	if total < 40 {
		total = 40
	}
	left := total * 55 / 100
	if left < 28 {
		left = 28
	}
	right := total - left
	if right < 20 {
		right = 20
	}
	return left, right
}

// contentHeight returns cH + sH (changes rows + stack rows).
// Layout rows: top(1) + cH + bottom(1) + top(1) + sH + bottom(1) + status(1) = height
// So cH + sH = height - 5
func (m model) contentHeight() int {
	h := m.height - 5
	if h < 4 {
		h = 4
	}
	return h
}

// rightPanelHeight returns the number of content rows in the right panel.
// Right panel spans from Changes top to Stack bottom: cH + 2 (border rows between panels) + sH
func (m model) rightPanelHeight() int {
	return m.changesHeight() + 2 + m.stackHeight()
}

func (m *model) clampStack() {
	vis := m.visibleItems()
	if len(vis) == 0 {
		m.cursor = 0
		m.scrollTop = 0
		return
	}
	if m.cursor >= len(vis) {
		m.cursor = len(vis) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.stackHeight()
	if m.cursor < m.scrollTop {
		m.scrollTop = m.cursor
	}
	if m.cursor >= m.scrollTop+h {
		m.scrollTop = m.cursor - h + 1
	}
	maxScroll := len(vis) - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollTop > maxScroll {
		m.scrollTop = maxScroll
	}
}

func (m *model) clampCommits() {
	n := len(m.visibleCommits())
	if n == 0 {
		m.commitCursor = 0
		m.commitScroll = 0
		return
	}
	if m.commitCursor >= n {
		m.commitCursor = n - 1
	}
	if m.commitCursor < 0 {
		m.commitCursor = 0
	}
	h := m.rightPanelHeight()
	if m.commitCursor < m.commitScroll {
		m.commitScroll = m.commitCursor
	}
	if m.commitCursor >= m.commitScroll+h {
		m.commitScroll = m.commitCursor - h + 1
	}
	maxScroll := n - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.commitScroll > maxScroll {
		m.commitScroll = maxScroll
	}
}

func (m *model) clampFiles() {
	n := len(m.visibleFiles())
	if n == 0 {
		m.fileCursor = 0
		m.fileScroll = 0
		return
	}
	if m.fileCursor >= n {
		m.fileCursor = n - 1
	}
	if m.fileCursor < 0 {
		m.fileCursor = 0
	}
	h := m.changesHeight()
	if m.fileCursor < m.fileScroll {
		m.fileScroll = m.fileCursor
	}
	if m.fileCursor >= m.fileScroll+h {
		m.fileScroll = m.fileCursor - h + 1
	}
	maxScroll := n - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.fileScroll > maxScroll {
		m.fileScroll = maxScroll
	}
}

// changesHeight returns how many content rows the Changes panel gets.
func (m model) changesHeight() int {
	h := m.contentHeight()
	nf := len(m.files)
	if nf == 0 {
		return 1
	}
	maxRows := h / 3
	if maxRows < 3 {
		maxRows = 3
	}
	// Ensure at least 2 rows for stack
	if maxRows > h-2 {
		maxRows = h - 2
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if nf < maxRows {
		return nf
	}
	return maxRows
}

// stackHeight returns how many content rows the Stack panel gets.
func (m model) stackHeight() int {
	sH := m.contentHeight() - m.changesHeight()
	if sH < 1 {
		sH = 1
	}
	return sH
}

// setFocus switches focus and tracks context for the right panel.
func (m *model) setFocus(p panel) {
	if p != panelRight && p != m.focus {
		m.rightCtx = p
	}
	m.focus = p
}

// rightShowsDiff returns true when the right panel shows a file diff,
// false when it shows commits.
func (m model) rightShowsDiff() bool {
	if m.focus == panelChanges {
		return true
	}
	if m.focus == panelStack {
		return false
	}
	// Right panel focused — use last left context
	return m.rightCtx == panelChanges
}

func (m model) selectedFilePath() string {
	if m.fileCursor < 0 || m.fileCursor >= len(m.files) {
		return ""
	}
	return m.files[m.fileCursor].Path
}

func (m *model) updateSubmitLabel() {
	label := "PR title"
	var flags []string
	if m.submitDraft {
		flags = append(flags, "DRAFT")
	}
	if m.submitForce {
		flags = append(flags, "FORCE")
	}
	if len(flags) > 0 {
		label += " (" + strings.Join(flags, "+") + ")"
	}
	m.inputLabel = label
}

func (m model) loadDiffForSelected() tea.Cmd {
	f := m.selectedFilePath()
	if f == "" {
		return nil
	}
	// Prefer staged diff if file is staged, otherwise unstaged
	idx := m.fileCursor
	if idx >= len(m.files) {
		return nil
	}
	file := m.files[idx]
	staged := file.isStaged() && !file.isUnstaged()
	return func() tea.Msg {
		diff := loadFileDiff(f, staged)
		return diffLoaded{path: f, diff: diff}
	}
}

func newModel() model {
	return model{loading: true, repo: repoName()}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(doLoad, tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return tickRefresh{}
	})
}

func doLoad() tea.Msg {
	if !isGitRepo() {
		return noGitRepoMsg{}
	}

	cur := currentBranch()

	// No commits yet — git-spice commands won't work, but we can still
	// show the trunk (from gs config) and the current branch.
	files := loadFileStatuses()

	if !hasCommits() {
		trunk := gsTrunkName()
		return loadResult{
			curBranch:      cur,
			noCommits:      true,
			noCommitsTrunk: trunk,
			files:          files,
		}
	}

	bs, err := loadBranches()
	if err != nil {
		return errMsg{err}
	}

	tracked := make(map[string]bool, len(bs))
	for _, b := range bs {
		tracked[b.Name] = true
	}

	allOut, _ := exec.Command(
		"git", "for-each-ref",
		"--sort=-committerdate",
		"--format=%(refname:short)",
		"refs/heads/",
	).Output()

	var untracked []string
	for _, line := range strings.Split(strings.TrimSpace(string(allOut)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && !tracked[name] {
			untracked = append(untracked, name)
		}
	}

	bases := detectBranchBases(untracked)

	return loadResult{branches: bs, untracked: untracked, bases: bases, curBranch: cur, files: files}
}

func buildItems(branches []*gsBranch, untracked []string, bases map[string]string, curBranch string) []item {
	// Build unified tree (tracked + detected untracked chains).
	tree := buildUnifiedTree(branches, bases)
	var nodes []node
	if tree != nil {
		nodes = flattenTree(tree, curBranch)
	}

	// Figure out which untracked branches are in the tree (have a detected base
	// that eventually chains to a tracked branch).
	inTree := make(map[string]bool)
	for _, n := range nodes {
		if n.isUntracked {
			inTree[n.uName] = true
		}
	}

	// Orphan untracked branches (no detected relationship to tracked tree).
	var orphans []string
	for _, name := range untracked {
		if !inTree[name] {
			orphans = append(orphans, name)
		}
	}

	items := make([]item, 0, len(nodes)+len(orphans)+1)
	for i := range nodes {
		items = append(items, item{node: &nodes[i]})
	}
	if len(orphans) > 0 {
		items = append(items, item{isSep: true})
		for _, name := range orphans {
			items = append(items, item{untrackedName: name})
		}
	}
	return items
}

func (m model) loadCommitsForSelected() tea.Cmd {
	it := m.sel()
	if it == nil {
		return nil
	}
	name := it.name()
	if name == "" {
		return nil
	}
	var base string
	if it.node != nil && !it.node.isUntracked && it.node.branch != nil && it.node.branch.Down != nil {
		base = it.node.branch.Down.Name
	} else if b, ok := m.detectedBases[name]; ok {
		base = b
	}
	return func() tea.Msg {
		commits, _ := loadCommits(name, base, 200)
		return commitsLoaded{branch: name, commits: commits}
	}
}

// ---------- update ----------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case noGitRepoMsg:
		m.loading = false
		m.noGitRepo = true

	case noGitSpiceMsg:
		// git init succeeded, now run gs repo init interactively
		m.noGitRepo = false
		m.lastCmd = "git-spice repo init"
		return m, m.runGS("repo", "init")

	case loadResult:
		m.loading = false
		m.noGitRepo = false
		m.notInited = false
		m.noCommits = msg.noCommits
		m.err = nil
		m.showHelp = false
		m.splitMode = false
		m.splitMarkers = nil
		m.splitBranch = ""
		m.detectedBases = msg.bases
		m.files = msg.files
		m.fileCursor = 0
		m.fileScroll = 0
		m.fileDiff = ""
		m.fileDiffScroll = 0

		if msg.noCommits {
			// No commits — show trunk (from gs config) + current branch.
			var items []item
			trunk := msg.noCommitsTrunk
			if trunk == "" {
				trunk = "main"
			}

			// Trunk node
			trunkNode := node{
				branch:  &gsBranch{Name: trunk, Current: trunk == msg.curBranch},
				isTrunk: true,
			}
			items = append(items, item{node: &trunkNode})

			// Current branch if different from trunk
			if msg.curBranch != "" && msg.curBranch != trunk {
				curNode := node{
					isUntracked: true,
					uName:       msg.curBranch,
					uIsCurrent:  true,
					connector:   "└─ ",
				}
				items = append(items, item{node: &curNode})
			}

			m.items = items
			m.commits = nil
		} else {
			m.items = buildItems(msg.branches, msg.untracked, msg.bases, msg.curBranch)
		}

		m.cursor = 0
		m.scrollTop = 0
		for i, it := range m.items {
			if it.node != nil && !it.node.isUntracked && it.node.branch.Current {
				m.cursor = i
				break
			}
			if it.node != nil && it.node.isUntracked && it.node.uIsCurrent {
				m.cursor = i
				break
			}
		}
		m.clampStack()
		if sel := m.sel(); sel != nil {
			m.selectedBranch = sel.name()
		}
		// Start focused on Stack (primary panel) unless there are changes
		if len(m.files) > 0 {
			m.setFocus(panelChanges)
		} else {
			m.setFocus(panelStack)
		}
		var cmds []tea.Cmd
		if len(m.files) > 0 {
			cmds = append(cmds, m.loadDiffForSelected())
		}
		if msg.noCommits {
			return m, tea.Batch(cmds...)
		}
		cmds = append(cmds, m.loadCommitsForSelected())
		return m, tea.Batch(cmds...)

	case commitsLoaded:
		if sel := m.sel(); sel != nil && sel.name() == msg.branch {
			m.commits = msg.commits
			m.commitCursor = 0
			m.commitScroll = 0
		}

	case filesReloaded:
		m.files = msg.files
		m.clampFiles()
		return m, m.loadDiffForSelected()

	case diffLoaded:
		m.fileDiff = msg.diff
		m.fileDiffScroll = 0

	case opDoneMsg:
		m.loading = true
		return m, doLoad

	case insertCreatedMsg:
		// Step 2 of insert: move the old child onto the newly created branch.
		newBranch := currentBranch()
		if newBranch == "" || msg.child == "" {
			m.loading = true
			return m, doLoad
		}
		m.lastCmd = "git-spice upstack onto --branch " + msg.child + " " + newBranch
		return m, m.runGSBg("upstack", "onto", "--branch", msg.child, newBranch)

	case needsRestackMsg:
		m.confirmMsg = msg.branch + " needs restacking. Restack and retry submit?"
		m.confirmArgs = msg.args
		m.confirmBranch = msg.branch

	case errMsg:
		m.loading = false
		if isNotInitedError(msg.err) {
			m.notInited = true
			m.err = nil
		} else {
			m.err = msg.err
		}

	case tickRefresh:
		if !m.loading && !m.noGitRepo && m.err == nil {
			newFiles := loadFileStatuses()
			filesChanged := len(newFiles) != len(m.files)
			if !filesChanged {
				for i := range newFiles {
					if newFiles[i] != m.files[i] {
						filesChanged = true
						break
					}
				}
			}
			if filesChanged {
				m.files = newFiles
				m.clampFiles()
			}
		}
		return m, tickCmd()

	case tea.KeyMsg:
		if m.loading {
			break
		}

		if m.noGitRepo {
			switch msg.String() {
			case "i":
				m.lastCmd = "git init + git-spice repo init"
				cmd := exec.Command("git", "init")
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					if err != nil {
						return errMsg{err}
					}
					// Chain: now init git-spice
					return noGitSpiceMsg{}
				})
			case "q", "ctrl+c", "esc":
				return m, tea.Quit
			}
			break
		}

		if m.notInited {
			switch msg.String() {
			case "i":
				m.lastCmd = "git-spice repo init"
				return m, m.runGS("repo", "init")
			case "q", "ctrl+c", "esc":
				return m, tea.Quit
			}
			break
		}

		if m.err != nil {
			switch msg.String() {
			case "q", "ctrl+c":
				return m, tea.Quit
			default:
				m.err = nil
				m.loading = true
				return m, doLoad
			}
		}

		// Confirm prompt (y/n)
		if m.confirmMsg != "" {
			switch msg.String() {
			case "y":
				branch := m.confirmBranch
				args := m.confirmArgs
				m.confirmMsg = ""
				m.confirmArgs = nil
				m.confirmBranch = ""
				m.lastCmd = "restack + submit " + branch
				return m, func() tea.Msg {
					gsBgCmd("branch", "restack", "--branch", branch).CombinedOutput()
					args = append(args, "--force")
					cmd := gsBgCmd(args...)
					out, err := cmd.CombinedOutput()
					if err != nil {
						msg := strings.TrimSpace(string(out))
						if msg != "" {
							return errMsg{fmt.Errorf("%s", msg)}
						}
						return errMsg{err}
					}
					return opDoneMsg{}
				}
			case "n", "esc":
				m.confirmMsg = ""
				m.confirmArgs = nil
				m.confirmBranch = ""
			}
			return m, nil
		}

		// Branch picker
		// Pending operation mode: user is picking a target branch inline.
		// Only intercept esc (cancel) and enter (confirm). All other keys
		// fall through to normal navigation so the user can move around.
		if m.pendingOp != "" {
			switch msg.String() {
			case "esc":
				m.pendingOp = ""
				m.pendingBranch = ""
				return m, nil
			case "enter", " ":
				target := ""
				if it := m.sel(); it != nil {
					target = it.name()
				}
				if target != "" && target != m.pendingBranch {
					branch := m.pendingBranch
					m.lastCmd = "git-spice upstack onto --branch " + branch + " " + target
					m.pendingOp = ""
					m.pendingBranch = ""
					return m, m.runGSBg("upstack", "onto", "--branch", branch, target)
				}
				return m, nil
			}
			// Everything else (arrows/tab etc.) falls through below.
		}

		// Split mode: marking commits to split a branch
		if m.splitMode && m.inputMode == "" {
			switch msg.String() {
			case "esc":
				m.splitMode = false
				m.splitBranch = ""
				m.splitMarkers = nil
				return m, nil
			case "down":
				m.commitCursor++
				m.clampCommits()
			case "up":
				m.commitCursor--
				m.clampCommits()
			case "s":
				idx := m.commitCursor
				if _, exists := m.splitMarkers[idx]; exists {
					delete(m.splitMarkers, idx)
				} else {
					m.inputMode = "split-name"
					m.inputLabel = "Branch name for split"
					m.inputBuf = ""
				}
			case "enter":
				if len(m.splitMarkers) > 0 {
					args := []string{"branch", "split", "--branch", m.splitBranch}
					// Sort by index descending (oldest commits first for gs)
					sorted := make([]int, 0, len(m.splitMarkers))
					for idx := range m.splitMarkers {
						sorted = append(sorted, idx)
					}
					for i := 0; i < len(sorted); i++ {
						for j := i + 1; j < len(sorted); j++ {
							if sorted[i] < sorted[j] {
								sorted[i], sorted[j] = sorted[j], sorted[i]
							}
						}
					}
					for _, idx := range sorted {
						args = append(args, "--at", m.commits[idx].Hash+":"+m.splitMarkers[idx])
					}
					m.lastCmd = "git-spice branch split"
					m.splitMode = false
					m.splitBranch = ""
					m.splitMarkers = nil
					return m, m.runGSBg(args...)
				}
			}
			return m, nil
		}

		// Inline text input mode (branch name, commit message, etc.)
		if m.inputMode != "" {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.inputMode = ""
				m.inputBuf = ""
				m.inputLabel = ""
			case "enter":
				text := strings.TrimSpace(m.inputBuf)
				mode := m.inputMode
				m.inputMode = ""
				m.inputBuf = ""
				m.inputLabel = ""
				if text == "" {
					break
				}
				switch mode {
				case "create-branch":
					return m, m.createBranchFallback(text)
				case "split-name":
					m.splitMarkers[m.commitCursor] = text
				case "rename":
					old := m.pendingBranch
					m.pendingBranch = ""
					m.lastCmd = "git-spice branch rename " + old + " " + text
					return m, m.runGSBg("branch", "rename", old, text)
				case "commit":
					m.lastCmd = "git-spice commit create"
					return m, func() tea.Msg {
						ensureTrunk()
						cmd := gsBgCmd("commit", "create", "-m", text)
						out, err := cmd.CombinedOutput()
						if err != nil {
							msg := strings.TrimSpace(string(out))
							if msg != "" {
								return errMsg{fmt.Errorf("%s", msg)}
							}
							return errMsg{err}
						}
						return opDoneMsg{}
					}
				case "amend":
					m.lastCmd = "git-spice commit amend"
					return m, func() tea.Msg {
						ensureTrunk()
						cmd := gsBgCmd("commit", "amend", "-m", text)
						out, err := cmd.CombinedOutput()
						if err != nil {
							msg := strings.TrimSpace(string(out))
							if msg != "" {
								return errMsg{fmt.Errorf("%s", msg)}
							}
							return errMsg{err}
						}
						return opDoneMsg{}
					}
				case "submit":
					branch := m.pendingBranch
					m.pendingBranch = ""
					draft := m.submitDraft
					force := m.submitForce
					m.lastCmd = "git-spice branch submit " + branch
					return m, func() tea.Msg {
						args := []string{"branch", "submit", "--branch", branch}
						if text != "" {
							args = append(args, "--title", text, "--fill")
						} else {
							args = append(args, "--fill")
						}
						if draft {
							args = append(args, "--draft")
						}
						if force {
							args = append(args, "--force")
						}
						cmd := gsBgCmd(args...)
						out, err := cmd.CombinedOutput()
						if err != nil {
							msg := strings.TrimSpace(string(out))
							if strings.Contains(msg, "needs to be restacked") && !force {
								return needsRestackMsg{branch: branch, args: args}
							}
							if msg != "" {
								return errMsg{fmt.Errorf("%s", msg)}
							}
							return errMsg{err}
						}
						return opDoneMsg{}
					}
				case "submit-stack":
					draft := m.submitDraft
					force := m.submitForce
					m.lastCmd = "git-spice stack submit"
					return m, func() tea.Msg {
						args := []string{"stack", "submit", "--fill"}
						if draft {
							args = append(args, "--draft")
						}
						if force {
							args = append(args, "--force")
						}
						cmd := gsBgCmd(args...)
						out, err := cmd.CombinedOutput()
						if err != nil {
							msg := strings.TrimSpace(string(out))
							if msg != "" {
								return errMsg{fmt.Errorf("%s", msg)}
							}
							return errMsg{err}
						}
						return opDoneMsg{}
					}
				}
			case "ctrl+d":
				if m.inputMode == "submit" || m.inputMode == "submit-stack" {
					m.submitDraft = !m.submitDraft
					m.updateSubmitLabel()
				}
			case "ctrl+f":
				if m.inputMode == "submit" || m.inputMode == "submit-stack" {
					m.submitForce = !m.submitForce
					m.updateSubmitLabel()
				}
			case "backspace", "ctrl+h":
				if len(m.inputBuf) > 0 {
					m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
				}
			default:
				if len(msg.Runes) == 1 {
					r := msg.Runes[0]
					if m.inputMode == "create-branch" || m.inputMode == "split-name" || m.inputMode == "rename" {
						// Only allow valid branch name characters
						if r != ' ' {
							m.inputBuf += string(r)
						}
					} else {
						m.inputBuf += string(r)
					}
				}
			}
			return m, nil
		}

		// Help overlay
		if m.showHelp {
			if msg.String() == "?" || msg.String() == "esc" || msg.String() == "q" {
				m.showHelp = false
				return m, nil
			}
			m.showHelp = false
		}

		// Search mode — contextual per panel
		if m.searching {
			switch msg.String() {
			case "esc":
				m.searching = false
				m.query = ""
				switch m.focus {
				case panelStack:
					m.clampStack()
					return m, m.loadCommitsForSelected()
				case panelChanges:
					m.clampFiles()
					return m, m.loadDiffForSelected()
				case panelRight:
					m.clampCommits()
				}
				return m, nil
			case "enter":
				m.searching = false
				switch m.focus {
				case panelStack:
					m.clampStack()
					return m, m.loadCommitsForSelected()
				case panelChanges:
					m.clampFiles()
					return m, m.loadDiffForSelected()
				case panelRight:
					m.clampCommits()
				}
				return m, nil
			case "backspace", "ctrl+h":
				if len(m.query) > 0 {
					m.query = m.query[:len(m.query)-1]
				}
			default:
				if len(msg.Runes) == 1 {
					m.query += string(msg.Runes)
				}
			}
			switch m.focus {
			case panelStack:
				m.cursor = 0
				m.scrollTop = 0
				m.clampStack()
			case panelChanges:
				m.fileCursor = 0
				m.fileScroll = 0
				m.clampFiles()
			case panelRight:
				if m.rightShowsDiff() {
					// Jump to first matching line in diff
					if m.query != "" {
						q := strings.ToLower(m.query)
						for i, line := range strings.Split(m.fileDiff, "\n") {
							if strings.Contains(strings.ToLower(line), q) {
								m.fileDiffScroll = i
								break
							}
						}
					}
				} else {
					m.commitCursor = 0
					m.commitScroll = 0
					m.clampCommits()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		// Panel switching
		case "tab":
			// tab switches between left column and right column
			if m.focus == panelRight {
				if m.rightCtx == panelChanges {
					m.setFocus(panelChanges)
				} else {
					m.setFocus(panelStack)
				}
			} else {
				m.setFocus(panelRight)
			}
		case "shift+tab":
			// same as tab — toggle columns
			if m.focus == panelRight {
				if m.rightCtx == panelChanges {
					m.setFocus(panelChanges)
				} else {
					m.setFocus(panelStack)
				}
			} else {
				m.setFocus(panelRight)
			}
		case "1":
			m.setFocus(panelChanges)
		case "2":
			m.setFocus(panelStack)
		case "3":
			m.setFocus(panelRight)
		case "left":
			// left/right switch between panels in same column (Changes ↔ Stack)
			if m.focus == panelStack {
				m.setFocus(panelChanges)
			}
		case "right":
			if m.focus == panelChanges {
				m.setFocus(panelStack)
			}

		case "/":
			m.searching = true
			m.query = ""
			// Reset cursor for the current panel
			switch m.focus {
			case panelChanges:
				m.fileCursor = 0
				m.fileScroll = 0
			case panelStack:
				m.cursor = 0
				m.scrollTop = 0
			case panelRight:
				if !m.rightShowsDiff() {
					m.commitCursor = 0
					m.commitScroll = 0
				}
			}
			return m, nil

		case "?":
			m.showHelp = true

		case "down":
			switch m.focus {
			case panelChanges:
				if m.fileCursor < len(m.files)-1 {
					m.fileCursor++
					m.clampFiles()
					return m, m.loadDiffForSelected()
				}
			case panelStack:
				prevName := ""
				if s := m.sel(); s != nil {
					prevName = s.name()
				}
				m.cursor = m.nextSelectable(m.cursor + 1)
				m.clampStack()
				if s := m.sel(); s != nil && s.name() != prevName {
					m.selectedBranch = s.name()
					return m, m.loadCommitsForSelected()
				}
			case panelRight:
				if m.rightShowsDiff() {
					m.fileDiffScroll++
				} else {
					m.commitCursor++
					m.clampCommits()
				}
			}

		case "up":
			switch m.focus {
			case panelChanges:
				if m.fileCursor > 0 {
					m.fileCursor--
					m.clampFiles()
					return m, m.loadDiffForSelected()
				}
			case panelStack:
				prevName := ""
				if s := m.sel(); s != nil {
					prevName = s.name()
				}
				m.cursor = m.prevSelectable(m.cursor - 1)
				m.clampStack()
				if s := m.sel(); s != nil && s.name() != prevName {
					m.selectedBranch = s.name()
					return m, m.loadCommitsForSelected()
				}
			case panelRight:
				if m.rightShowsDiff() {
					if m.fileDiffScroll > 0 {
						m.fileDiffScroll--
					}
				} else {
					m.commitCursor--
					m.clampCommits()
				}
			}

		case "home":
			switch m.focus {
			case panelChanges:
				m.fileCursor = 0
				m.clampFiles()
				return m, m.loadDiffForSelected()
			case panelStack:
				m.cursor = m.nextSelectable(0)
				m.clampStack()
				return m, m.loadCommitsForSelected()
			case panelRight:
				if m.rightShowsDiff() {
					m.fileDiffScroll = 0
				} else {
					m.commitCursor = 0
					m.clampCommits()
				}
			}

		case "end":
			switch m.focus {
			case panelChanges:
				m.fileCursor = len(m.files) - 1
				m.clampFiles()
				return m, m.loadDiffForSelected()
			case panelStack:
				vis := m.visibleItems()
				m.cursor = m.prevSelectable(len(vis) - 1)
				m.clampStack()
				return m, m.loadCommitsForSelected()
			case panelRight:
				if m.rightShowsDiff() {
					m.fileDiffScroll = 99999
				} else {
					m.commitCursor = len(m.commits) - 1
					m.clampCommits()
				}
			}

		case " ":
			switch m.focus {
			case panelChanges:
				// Stage/unstage
				if m.fileCursor < len(m.files) {
					f := m.files[m.fileCursor]
					return m, m.stageToggle(f)
				}
			case panelStack:
				it := m.sel()
				if it == nil {
					break
				}
				if it.isUntrackedItem() {
					name := it.name()
					m.lastCmd = "git checkout " + name
					return m, m.gitCheckout(name)
				}
				if it.node != nil && !it.node.isTrunk {
					m.lastCmd = "git-spice branch checkout " + it.node.branch.Name
					return m, m.runGSBg("branch", "checkout", it.node.branch.Name)
				}
			}

		case "a":
			// Stage all
			if m.focus == panelChanges && len(m.files) > 0 {
				return m, func() tea.Msg {
					stageAll()
					return filesReloaded{files: loadFileStatuses()}
				}
			}

		case "c":
			if m.focus == panelChanges {
				m.inputMode = "commit"
				m.inputLabel = "Commit message"
				m.inputBuf = ""
				return m, nil
			}

		case "A":
			if m.focus == panelChanges {
				m.inputMode = "amend"
				m.inputLabel = "Amend message"
				m.inputBuf = ""
				return m, nil
			}

		case "D":
			if m.focus == panelChanges && m.fileCursor < len(m.files) {
				f := m.files[m.fileCursor]
				return m, m.discardFile(f)
			}

		// Branch operations (Stack panel)
		case "n":
			if m.focus == panelStack {
				if !hasCommits() {
					m.inputMode = "create-branch"
					m.inputLabel = "New branch name"
					m.inputBuf = ""
					m.setFocus(panelStack)
					return m, nil
				}
				m.lastCmd = "git-spice branch create"
				return m, m.runGS("branch", "create")
			}
		case "r":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.lastCmd = "git-spice branch restack " + n.branch.Name
				return m, m.runGSBg("branch", "restack", "--branch", n.branch.Name)
			}
		case "R":
			if n := m.selTracked(); n != nil && n.isTrunk {
				m.lastCmd = "git-spice repo restack"
				return m, m.runGSBg("repo", "restack")
			}
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.inputMode = "rename"
				m.inputLabel = "Rename " + n.branch.Name + " to"
				m.inputBuf = ""
				m.pendingBranch = n.branch.Name
				return m, nil
			}
		case "d":
			it := m.sel()
			if it != nil {
				m.lastCmd = "git-spice branch delete " + it.name()
				return m, m.runGSBg("branch", "delete", "--force", it.name())
			}
		case "s":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				// Pre-fill title from first commit subject
				title := ""
				if len(m.commits) > 0 {
					title = m.commits[0].Subject
				}
				m.inputMode = "submit"
				m.inputLabel = "PR title (d:draft)"
				m.inputBuf = title
				m.pendingBranch = n.branch.Name
				m.submitDraft = false
				m.submitForce = false
	return m, nil
			}
		case "S":
			m.lastCmd = "git-spice stack submit"
			return m, m.runGS("stack", "submit")
		case "e":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.lastCmd = "git-spice branch edit " + n.branch.Name
				return m, m.runGS("branch", "edit", n.branch.Name)
			}
		case "I":
			if n := m.selTracked(); n != nil && len(n.branch.Ups) > 0 {
				child := n.branch.Ups[0].Name
				m.lastCmd = "git-spice branch create (insert above " + n.branch.Name + ")"
				cmd := gsExecCmd("branch", "create")
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					if err != nil {
						return errMsg{err}
					}
					return insertCreatedMsg{child: child}
				})
			}
		case "b":
			if n := m.selTracked(); n != nil && !n.isTrunk && len(m.commits) > 1 {
				m.splitMode = true
				m.splitBranch = n.branch.Name
				m.splitMarkers = map[int]string{}
				m.setFocus(panelRight)
				m.rightCtx = panelStack
			}
		case "O":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.pendingOp = "onto"
				m.pendingBranch = n.branch.Name
				m.setFocus(panelStack)
				return m, nil
			}
		case "Q":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.lastCmd = "git-spice branch squash " + n.branch.Name
				return m, m.runGSBg("branch", "squash", "--branch", n.branch.Name)
			}
		case "f":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.lastCmd = "git-spice branch fold --branch " + n.branch.Name
				return m, m.runGSBg("branch", "fold", "--branch", n.branch.Name)
			}
		case "t":
			it := m.sel()
			if it == nil {
				break
			}
			name := it.name()
			if name != "" {
				m.lastCmd = "git-spice branch track " + name
				return m, func() tea.Msg {
					ensureTrunk()
					cmd := gsBgCmd("branch", "track", name)
					out, err := cmd.CombinedOutput()
					if err != nil {
						msg := strings.TrimSpace(string(out))
						if msg != "" {
							return errMsg{fmt.Errorf("%s", msg)}
						}
						return errMsg{err}
					}
					return opDoneMsg{}
				}
			}
		case "T":
			if n := m.selTracked(); n != nil && !n.isTrunk {
				m.lastCmd = "git-spice branch untrack " + n.branch.Name
				return m, m.runGSBg("branch", "untrack", n.branch.Name)
			}
		case "o":
			if n := m.selTracked(); n != nil && n.branch.Change != nil {
				return m, openURL(n.branch.Change.URL)
			}
		case "y":
			m.lastCmd = "git-spice repo sync"
			return m, m.runGSBg("repo", "sync")
		}
	}

	return m, nil
}

func (m model) stageToggle(f fileStatus) tea.Cmd {
	return func() tea.Msg {
		if f.isUnstaged() || f.isUntracked() {
			stageFile(f.Path)
		} else if f.isStaged() {
			unstageFile(f.Path)
		}
		return filesReloaded{files: loadFileStatuses()}
	}
}

func (m model) discardFile(f fileStatus) tea.Cmd {
	return func() tea.Msg {
		if f.isUntracked() {
			exec.Command("git", "clean", "-fd", "--", f.Path).Run()
		} else {
			exec.Command("git", "checkout", "--", f.Path).Run()
		}
		return filesReloaded{files: loadFileStatuses()}
	}
}

func (m model) nextSelectable(from int) int {
	vis := m.visibleItems()
	for i := from; i < len(vis); i++ {
		if vis[i].selectable() {
			return i
		}
	}
	return m.cursor
}

func (m model) prevSelectable(from int) int {
	vis := m.visibleItems()
	for i := from; i >= 0; i-- {
		if vis[i].selectable() {
			return i
		}
	}
	return m.cursor
}

// runGS runs a gs command interactively (suspends TUI for user input).
// Tees stdout+stderr so error messages survive the TUI restore.
func (m model) runGS(args ...string) tea.Cmd {
	cmd := gsExecCmd(args...)
	var outBuf strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &outBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &outBuf)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			msg := strings.TrimSpace(outBuf.String())
			if msg != "" {
				// Extract the most useful line (last non-empty line)
				lines := strings.Split(msg, "\n")
				for i := len(lines) - 1; i >= 0; i-- {
					l := strings.TrimSpace(lines[i])
					if l != "" && !strings.HasPrefix(l, "FTL ") {
						return errMsg{fmt.Errorf("%s", l)}
					}
				}
				return errMsg{fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))}
			}
			return errMsg{err}
		}
		return opDoneMsg{}
	})
}

// runGit runs a plain git command interactively (suspends TUI).
// Tees stdout+stderr so error messages survive the TUI restore.
func (m model) runGit(args ...string) tea.Cmd {
	cmd := exec.Command("git", args...)
	var outBuf strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &outBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &outBuf)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			msg := strings.TrimSpace(outBuf.String())
			if msg != "" {
				return errMsg{fmt.Errorf("%s", msg)}
			}
			return errMsg{err}
		}
		return opDoneMsg{}
	})
}

// runGSBg runs a gs command in the background without suspending the TUI.
func (m model) runGSBg(args ...string) tea.Cmd {
	return func() tea.Msg {
		cmd := gsBgCmd(args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Include command output in the error for context.
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return errMsg{fmt.Errorf("%s", msg)}
			}
			return errMsg{err}
		}
		return opDoneMsg{}
	}
}

func (m model) gitCheckout(branch string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "checkout", branch)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return errMsg{fmt.Errorf("%s", msg)}
			}
			return errMsg{err}
		}
		return opDoneMsg{}
	}
}

// createBranchFallback creates a branch with plain git (for repos without commits).
func (m model) createBranchFallback(name string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "checkout", "-b", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return errMsg{fmt.Errorf("%s", msg)}
			}
			return errMsg{err}
		}
		// Try to track with git-spice (may fail if no commits — that's ok)
		gsBgCmd("branch", "track", name).CombinedOutput()
		return opDoneMsg{}
	}
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		exec.Command("open", url).Run()
		return nil
	}
}

// ---------- view ----------

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.loading {
		return m.renderFrame(m.centeredMessage(stDim.Render("loading…")))
	}
	if m.noGitRepo {
		body := stAppTitle.Render("Welcome to spicygit") + "\n\n" +
			stDim.Render("No git repository found in this directory.") + "\n\n" +
			stKey.Render("i") + "  initialize git repo + git-spice\n" +
			stKey.Render("q") + "  quit"
		return m.renderFrame(m.centeredMessage(body))
	}
	if m.notInited {
		body := stAppTitle.Render("Welcome to spicygit") + "\n\n" +
			stDim.Render("Git repository found, but git-spice is not initialized.") + "\n\n" +
			stKey.Render("i") + "  initialize git-spice\n" +
			stKey.Render("q") + "  quit"
		return m.renderFrame(m.centeredMessage(body))
	}
	if m.err != nil {
		body := stErr.Render(m.err.Error()) + "\n\n" + stDim.Render("press any key to retry · q quit")
		return m.renderFrame(m.centeredMessage(body))
	}
	if m.showHelp {
		return m.overlayHelp()
	}
	return m.renderPanels()
}

func (m model) centeredMessage(body string) string {
	leftW, rightW := m.panelWidths()
	totalInner := leftW + rightW + 5 // inner width including borders and gap
	h := m.contentHeight()

	lines := strings.Split(body, "\n")
	var sb strings.Builder
	// Vertical centering
	topPad := (h - len(lines)) / 2
	if topPad < 0 {
		topPad = 0
	}
	for i := 0; i < topPad; i++ {
		sb.WriteString(strings.Repeat(" ", totalInner) + "\n")
	}
	for _, line := range lines {
		vis := lipgloss.Width(line)
		leftPad := (totalInner - vis) / 2
		if leftPad < 0 {
			leftPad = 0
		}
		rightPad := totalInner - vis - leftPad
		if rightPad < 0 {
			rightPad = 0
		}
		sb.WriteString(strings.Repeat(" ", leftPad) + line + strings.Repeat(" ", rightPad) + "\n")
	}
	remaining := h - topPad - len(lines)
	for i := 0; i < remaining; i++ {
		sb.WriteString(strings.Repeat(" ", totalInner) + "\n")
	}
	return sb.String()
}

// renderFrame wraps content in a simple full-width border (used for non-panel views).
func (m model) renderFrame(body string) string {
	leftW, rightW := m.panelWidths()
	totalInner := leftW + rightW + 5
	totalWidth := totalInner + 4 // │ sp [inner] sp │

	b := stBorder.Render
	title := stAppTitle.Render("spicygit")
	if m.repo != "" {
		title += stDim.Render("  ·  " + m.repo)
	}
	titleW := lipgloss.Width(title)

	// Top border
	fill := totalWidth - titleW - 3 // ┌─title─...─┐
	if fill < 1 {
		fill = 1
	}
	top := b("┌─") + title + b(strings.Repeat("─", fill)+"┐")

	// Bottom border
	bot := b("└" + strings.Repeat("─", totalWidth-2) + "┘")

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var sb strings.Builder
	sb.WriteString(top + "\n")
	for _, line := range lines {
		vis := lipgloss.Width(line)
		pad := totalInner - vis
		if pad < 0 {
			pad = 0
		}
		sb.WriteString(b("│") + " " + line + strings.Repeat(" ", pad) + " " + b("│") + "\n")
	}
	sb.WriteString(bot)
	return sb.String()
}

// renderPanels renders three independent bordered panels:
// Changes (top-left), Stack (bottom-left), and a context panel (right).
func (m model) renderPanels() string {
	leftW, rightW := m.panelWidths()
	cH := m.changesHeight()
	sH := m.stackHeight()

	// Render left panels
	changesLines := padToSize(m.renderChangesSection(leftW, cH), cH, leftW)
	stackLines := padToSize(m.renderStackSection(leftW, sH), sH, leftW)

	// Right panel context-switches based on last left panel focus
	rH := cH + 2 + sH // right panel content height (changes + 2 border rows + stack)
	var rightLines []string
	var rightLabel string
	if m.rightShowsDiff() {
		rightLines = m.renderDiffContent(rightW, rH)
		rightLabel = m.selectedFilePath()
		if rightLabel == "" {
			rightLabel = "Diff"
		}
	} else {
		rightLines = m.renderCommitContent(rightW, rH)
		rightLabel = m.selectedBranch
		if rightLabel == "" {
			rightLabel = "Commits"
		}
	}
	rightLines = padToSize(rightLines, rH, rightW)

	// Per-panel border functions
	cb := m.borderFunc(panelChanges)
	skb := m.borderFunc(panelStack)
	rb := m.borderFunc(panelRight)

	// Panel titles
	changesLabel := fmt.Sprintf("Changes (%d)", len(m.files))
	changesTitle := m.titleStr(panelChanges, changesLabel)
	stackTitle := m.titleStr(panelStack, "Stack")
	rightTitle := m.titleStr(panelRight, rightLabel)

	// Measure title widths for fill calculations
	changesTitleW := lipgloss.Width(changesTitle)
	stackTitleW := lipgloss.Width(stackTitle)
	rightTitleW := lipgloss.Width(rightTitle)

	changesFill := leftW + 2 - changesTitleW - 1
	if changesFill < 1 {
		changesFill = 1
	}
	stackFill := leftW + 2 - stackTitleW - 1
	if stackFill < 1 {
		stackFill = 1
	}
	rightFill := rightW + 2 - rightTitleW - 1
	if rightFill < 1 {
		rightFill = 1
	}

	var sb strings.Builder
	ri := 0 // right panel line index

	gap := " " // space between left and right columns

	// ── Changes top border ── + gap + Right top border ──
	sb.WriteString(
		cb("┌─") + changesTitle + cb(strings.Repeat("─", changesFill)+"┐") +
			gap +
			rb("┌─") + rightTitle + rb(strings.Repeat("─", rightFill)+"┐") +
			"\n",
	)

	// ── Changes content rows ──
	for i := 0; i < cH; i++ {
		sb.WriteString(
			cb("│") + " " + changesLines[i] + " " + cb("│") +
				gap +
				rb("│") + " " + rightLines[ri] + " " + rb("│") +
				"\n",
		)
		ri++
	}

	// ── Changes bottom border ── + gap + Right content row ──
	sb.WriteString(
		cb("└"+strings.Repeat("─", leftW+2)+"┘") +
			gap +
			rb("│") + " " + rightLines[ri] + " " + rb("│") +
			"\n",
	)
	ri++

	// ── Stack top border ── + gap + Right content row ──
	sb.WriteString(
		skb("┌─") + stackTitle + skb(strings.Repeat("─", stackFill)+"┐") +
			gap +
			rb("│") + " " + rightLines[ri] + " " + rb("│") +
			"\n",
	)
	ri++

	// ── Stack content rows ──
	for i := 0; i < sH; i++ {
		sb.WriteString(
			skb("│") + " " + stackLines[i] + " " + skb("│") +
				gap +
				rb("│") + " " + rightLines[ri] + " " + rb("│") +
				"\n",
		)
		ri++
	}

	// ── Stack bottom + gap + Right bottom ──
	sb.WriteString(
		skb("└"+strings.Repeat("─", leftW+2)+"┘") +
			gap +
			rb("└"+strings.Repeat("─", rightW+2)+"┘") +
			"\n",
	)

	// ── Status bar (below the frames) ──
	totalW := leftW + rightW + 9 // total width including borders and gap
	status := m.renderStatusContent(totalW - 1)
	sb.WriteString(" " + status)

	return sb.String()
}

func (m model) borderFunc(p panel) func(string) string {
	style := stBorder
	if m.focus == p {
		style = stBorderActive
	}
	return func(s string) string { return style.Render(s) }
}

func (m model) titleStr(p panel, text string) string {
	if m.focus == p {
		return stTitle.Render(text)
	}
	return stTitleInact.Render(text)
}

// ---------- left panel sections ----------

func (m model) renderChangesSection(width, maxRows int) []string {
	isActive := m.focus == panelChanges
	files := m.visibleFiles()

	if len(files) == 0 {
		if m.searching && m.focus == panelChanges && m.query != "" {
			return []string{stDim.Render("  no matches")}
		}
		label := stDim.Render("  clean")
		return []string{label}
	}

	end := m.fileScroll + maxRows
	if end > len(files) {
		end = len(files)
	}

	var lines []string
	for i := m.fileScroll; i < end; i++ {
		f := files[i]

		// Status indicator
		var indicator string
		if f.isUntracked() {
			indicator = stRestack.Render("??")
		} else {
			ix := string(f.Index)
			wt := string(f.WorkTree)
			if f.isStaged() {
				ix = stPROpen.Render(ix)
			} else {
				ix = stDim.Render(ix)
			}
			if f.isUnstaged() {
				wt = stErr.Render(wt)
			} else {
				wt = stDim.Render(wt)
			}
			indicator = ix + wt
		}

		line := indicator + " " + f.Path

		if i == m.fileCursor && isActive {
			content := "▸ " + line
			v := lipgloss.Width(content)
			if v < width {
				content += strings.Repeat(" ", width-v)
			}
			lines = append(lines, stSel.Render(content))
		} else if i == m.fileCursor {
			lines = append(lines, stDim.Render("▸ ")+line)
		} else {
			lines = append(lines, "  "+line)
		}
	}

	// Search bar
	if m.searching && m.focus == panelChanges {
		if len(lines) >= maxRows {
			lines = lines[:maxRows-1]
		}
		searchLine := stKey.Render("/") + m.query + "█"
		lines = append(lines, searchLine)
	}

	return lines
}

func (m model) renderStackSection(width, maxRows int) []string {
	vis := m.visibleItems()
	total := len(vis)

	end := m.scrollTop + maxRows
	if end > total {
		end = total
	}

	isActive := m.focus == panelStack
	var lines []string

	for i := m.scrollTop; i < end; i++ {
		it := vis[i]

		if it.isSep {
			sep := stDim.Render("── untracked " + strings.Repeat("─", imax(0, width-13)))
			lines = append(lines, sep)
			continue
		}

		var line string
		if it.node != nil {
			line = renderNode(*it.node)
		} else {
			line = stUntracked.Render(it.untrackedName)
		}

		isPending := m.pendingOp != "" && it.name() == m.pendingBranch
		if isPending {
			line = stWarning.Render("◆ ") + line
		}

		if i == m.cursor && isActive {
			prefix := "▸ "
			if isPending {
				prefix = ""
			}
			content := prefix + line
			v := lipgloss.Width(content)
			if v < width {
				content += strings.Repeat(" ", width-v)
			}
			lines = append(lines, stSel.Render(content))
		} else if i == m.cursor {
			if isPending {
				lines = append(lines, line)
			} else {
				lines = append(lines, stDim.Render("▸ ")+line)
			}
		} else {
			if isPending {
				lines = append(lines, line)
			} else {
				lines = append(lines, "  "+line)
			}
		}
	}

	// Hint for empty repos
	if m.noCommits && !m.searching {
		lines = append(lines, "")
		lines = append(lines, stDim.Render("  No commits yet — ")+stKey.Render("c")+stDim.Render(" to commit"))
	}

	// Search bar
	if m.searching {
		if len(lines) >= maxRows {
			lines = lines[:maxRows-1]
		}
		searchLine := stKey.Render("/") + m.query + "█"
		lines = append(lines, searchLine)
	}

	return lines
}

// ---------- diff panel content ----------

func (m model) renderDiffContent(width, height int) []string {
	if m.fileDiff == "" {
		if len(m.files) == 0 {
			return []string{stDim.Render("no changes")}
		}
		return []string{stDim.Render("select a file to view diff")}
	}

	allLines := strings.Split(m.fileDiff, "\n")
	start := m.fileDiffScroll
	if start >= len(allLines) {
		start = len(allLines) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > len(allLines) {
		end = len(allLines)
	}

	var lines []string
	for _, raw := range allLines[start:end] {
		// Truncate to width
		if len(raw) > width {
			raw = raw[:width]
		}
		// Colorize diff lines
		switch {
		case strings.HasPrefix(raw, "+++ ") || strings.HasPrefix(raw, "--- "):
			lines = append(lines, stDim.Render(raw))
		case strings.HasPrefix(raw, "+"):
			lines = append(lines, stPROpen.Render(raw))
		case strings.HasPrefix(raw, "-"):
			lines = append(lines, stErr.Render(raw))
		case strings.HasPrefix(raw, "@@"):
			lines = append(lines, stCurrent.Render(raw))
		case strings.HasPrefix(raw, "diff "):
			lines = append(lines, lipgloss.NewStyle().Bold(true).Render(raw))
		default:
			lines = append(lines, raw)
		}
	}
	return lines
}

// ---------- commit panel content ----------

func (m model) renderCommitContent(width, height int) []string {
	commits := m.visibleCommits()
	if len(commits) == 0 {
		if m.searching && m.focus == panelRight && m.query != "" {
			return []string{stDim.Render("no matches")}
		}
		empty := stDim.Render("no commits")
		return []string{empty}
	}

	isActive := m.focus == panelRight
	total := len(commits)
	end := m.commitScroll + height
	if end > total {
		end = total
	}

	// Column widths
	hashW := 7
	timeW := 14 // "2 hours ago" etc
	subjectW := width - hashW - timeW - 5 // spaces between columns
	if subjectW < 10 {
		subjectW = 10
	}

	var lines []string
	for i := m.commitScroll; i < end && len(lines) < height; i++ {
		c := commits[i]

		hash := stHash.Render(truncate(c.Hash, hashW))
		subject := truncate(c.Subject, subjectW)
		time := stTime.Render(truncLeft(c.Time, timeW))

		if i == m.commitCursor && isActive {
			full := "▸ " + hash + "  " + subject
			vis := lipgloss.Width(full)
			gap := width - vis - lipgloss.Width(time)
			if gap < 1 {
				gap = 1
			}
			row := full + strings.Repeat(" ", gap) + time
			rowVis := lipgloss.Width(row)
			if rowVis < width {
				row += strings.Repeat(" ", width-rowVis)
			}
			lines = append(lines, stSel.Render(row))
		} else if i == m.commitCursor && !isActive {
			full := stDim.Render("▸ ") + hash + "  " + subject
			vis := lipgloss.Width(full)
			gap := width - vis - lipgloss.Width(time)
			if gap < 1 {
				gap = 1
			}
			lines = append(lines, full+strings.Repeat(" ", gap)+time)
		} else {
			full := "  " + hash + "  " + subject
			vis := lipgloss.Width(full)
			gap := width - vis - lipgloss.Width(time)
			if gap < 1 {
				gap = 1
			}
			lines = append(lines, full+strings.Repeat(" ", gap)+time)
		}

		// Show split marker after this commit
		if m.splitMode {
			if name, ok := m.splitMarkers[i]; ok && len(lines) < height {
				label := " ✂ " + name + " "
				fill := width - lipgloss.Width(label) - 4
				if fill < 1 {
					fill = 1
				}
				sep := "  " + stWarning.Render("──"+label+strings.Repeat("─", fill))
				lines = append(lines, sep)
			}
		}
	}

	return lines
}

// ---------- status bar ----------

func (m model) renderStatusContent(width int) string {
	if m.confirmMsg != "" {
		return stWarning.Render(m.confirmMsg) + "  " +
			stKey.Render("y") + stDesc.Render(" yes") +
			stDim.Render(" · ") +
			stKey.Render("n") + stDesc.Render(" no")
	}

	if m.inputMode != "" {
		return stKey.Render(m.inputLabel+": ") + m.inputBuf + "█" +
			stDim.Render("    enter confirm · esc cancel")
	}

	if m.splitMode {
		n := len(m.splitMarkers)
		count := stWarning.Render(fmt.Sprintf("%d split(s)", n))
		return stWarning.Render("Split: ") +
			stCurrent.Render(m.splitBranch) +
			stDim.Render(" │ ") +
			count +
			stDim.Render(" │ ") +
			stKey.Render("s") + stDesc.Render(" mark/unmark") +
			stDim.Render(" │ ") +
			stKey.Render("enter") + stDesc.Render(" execute") +
			stDim.Render(" │ ") +
			stKey.Render("esc") + stDesc.Render(" cancel")
	}

	if m.searching {
		return stDesc.Render("Cancel: ") + stKey.Render("esc") +
			stDesc.Render(" │ ") +
			stDesc.Render("Confirm: ") + stKey.Render("enter")
	}

	if m.pendingOp != "" {
		return stWarning.Render("Moving ") +
			stCurrent.Render(m.pendingBranch) +
			stWarning.Render(" onto → select target, ") +
			stKey.Render("enter") + stDesc.Render(" confirm · ") +
			stKey.Render("esc") + stDesc.Render(" cancel")
	}

	if m.noCommits {
		parts := []string{
			stDesc.Render("Commit: ") + stKey.Render("c"),
			stDesc.Render("New branch: ") + stKey.Render("n"),
		}
		if len(m.files) > 0 && m.focus == panelChanges {
			parts = append(parts,
				stDesc.Render("Stage/unstage: ")+stKey.Render("space"),
				stDesc.Render("Stage all: ")+stKey.Render("a"),
			)
		}
		parts = append(parts, stDesc.Render("Keybindings: ")+stKey.Render("?"))
		return strings.Join(parts, stDim.Render(" │ "))
	}

	// Context-sensitive hints based on section and selected item
	it := m.sel()
	var parts []string

	if m.focus == panelChanges {
		if len(m.files) > 0 {
			parts = append(parts,
				stDesc.Render("Stage/unstage: ")+stKey.Render("space"),
				stDesc.Render("Stage all: ")+stKey.Render("a"),
			)
		}
		parts = append(parts,
			stDesc.Render("Commit: ")+stKey.Render("c"),
			stDesc.Render("Keybindings: ")+stKey.Render("?"),
		)
		return strings.Join(parts, stDim.Render(" │ "))
	}

	if it != nil && it.isUntrackedItem() {
		parts = append(parts,
			stDesc.Render("Checkout: ")+stKey.Render("space"),
			stDesc.Render("Track: ")+stKey.Render("t"),
			stDesc.Render("Delete: ")+stKey.Render("d"),
		)
	} else if it != nil && it.node != nil && it.node.isTrunk {
		parts = append(parts,
			stDesc.Render("New branch: ")+stKey.Render("n"),
			stDesc.Render("Sync: ")+stKey.Render("y"),
			stDesc.Render("Restack all: ")+stKey.Render("R"),
		)
	} else if it != nil && it.node != nil && !it.node.isTrunk {
		parts = append(parts,
			stDesc.Render("Checkout: ")+stKey.Render("space"),
			stDesc.Render("Restack: ")+stKey.Render("r"),
			stDesc.Render("Submit: ")+stKey.Render("s"),
			stDesc.Render("Submit stack: ")+stKey.Render("S"),
			stDesc.Render("New branch: ")+stKey.Render("n"),
		)
	}

	// Always show these
	parts = append(parts,
		stDesc.Render("Keybindings: ")+stKey.Render("?"),
	)

	hints := strings.Join(parts, stDim.Render(" │ "))

	if m.lastCmd == "" {
		return hints
	}

	// Right-align last command
	cmdStr := stDim.Render("$ " + m.lastCmd)
	hintsW := lipgloss.Width(hints)
	cmdW := lipgloss.Width(cmdStr)
	gap := width - hintsW - cmdW
	if gap < 2 {
		return hints
	}
	return hints + strings.Repeat(" ", gap) + cmdStr
}

// ---------- help overlay ----------

// overlayHelp renders the help popover centered on top of the background.
// padTo pads s with spaces to exactly w visual columns.
func padTo(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vis)
}

// overlayHelp renders a bordered popover on top of the panel background.
func (m model) overlayHelp() string {
	bg := m.renderPanels()
	it := m.sel()

	const keyCol = 8 // right-aligned key column width

	type row struct {
		key  string
		desc string
		sep  string
	}

	var rows []row
	rows = append(rows, row{sep: "Navigation"})
	rows = append(rows,
		row{key: "↑/↓", desc: "navigate items"},
		row{key: "←/→", desc: "switch panel in column"},
		row{key: "tab", desc: "switch column"},
		row{key: "1/2/3", desc: "jump to panel"},
		row{key: "/", desc: "search branches"},
		row{key: "?", desc: "close this help"},
		row{key: "q", desc: "quit"},
	)

	// Changes section keys
	cks := changesKeys()
	rows = append(rows, row{sep: "Changes"})
	for _, k := range cks {
		rows = append(rows, row{key: k.key, desc: k.desc})
	}

	if ks := keysFor(it); len(ks) > 0 {
		rows = append(rows, row{sep: "Branch Actions"})
		for _, k := range ks {
			rows = append(rows, row{key: k.key, desc: k.desc})
		}
	}

	// Measure content to determine box width.
	maxContent := 0
	for _, r := range rows {
		var w int
		if r.sep != "" {
			w = len(r.sep) + 6 // "── sep ──"
		} else {
			w = keyCol + 3 + len(r.desc)
		}
		if w > maxContent {
			maxContent = w
		}
	}
	boxInner := maxContent + 4 // 2 padding each side
	if boxInner < 36 {
		boxInner = 36
	}
	if boxInner > m.width-4 {
		boxInner = m.width - 4
	}

	b := stBorderActive.Render
	blank := b("│") + strings.Repeat(" ", boxInner) + b("│")

	// Helper: wrap content line in borders, padded to boxInner.
	wrap := func(content string) string {
		return b("│") + padTo(content, boxInner) + b("│")
	}

	var popLines []string

	// Top border
	title := stTitle.Render(" Keybindings ")
	titleW := lipgloss.Width(title)
	topFill := boxInner - titleW
	if topFill < 0 {
		topFill = 0
	}
	popLines = append(popLines, b("┌")+title+b(strings.Repeat("─", topFill)+"┐"))
	popLines = append(popLines, blank)

	// Content rows
	for _, r := range rows {
		if r.sep != "" {
			sepText := " " + r.sep + " "
			dashTotal := boxInner - 4 - len(sepText) // 2 padding + dashes
			ld := dashTotal / 2
			rd := dashTotal - ld
			if ld < 1 {
				ld = 1
			}
			if rd < 1 {
				rd = 1
			}
			line := "  " + stDim.Render(strings.Repeat("─", ld)+sepText+strings.Repeat("─", rd)) + "  "
			popLines = append(popLines, wrap(line))
		} else {
			keyStr := stKey.Render(fmt.Sprintf("%*s", keyCol, r.key))
			descStr := stDesc.Render(r.desc)
			line := "  " + keyStr + "   " + descStr
			popLines = append(popLines, wrap(line))
		}
	}

	// Close hint
	popLines = append(popLines, blank)
	hint := stDim.Render("? or esc to close")
	hintW := lipgloss.Width(hint)
	hintPad := (boxInner - hintW) / 2
	if hintPad < 0 {
		hintPad = 0
	}
	popLines = append(popLines, wrap(strings.Repeat(" ", hintPad)+hint))
	popLines = append(popLines, blank)

	// Bottom border
	popLines = append(popLines, b("└"+strings.Repeat("─", boxInner)+"┘"))

	// Composite onto background
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < m.height {
		bgLines = append(bgLines, strings.Repeat(" ", m.width))
	}

	popH := len(popLines)
	popW := boxInner + 2
	startY := (m.height - popH) / 2
	if startY < 0 {
		startY = 0
	}
	startX := (m.width - popW) / 2
	if startX < 0 {
		startX = 0
	}

	// Splice popover into background, preserving ANSI-styled content on both sides.
	for i, popLine := range popLines {
		y := startY + i
		if y >= len(bgLines) {
			break
		}
		bg := bgLines[y]
		// Left portion: truncate background to startX visual columns.
		leftBg := ansi.Truncate(bg, startX, "")
		// Right portion: cut away startX+popW columns from the left, keep the rest.
		rightStart := startX + popW
		rightBg := ansi.TruncateLeft(bg, rightStart, "")
		bgLines[y] = leftBg + popLine + rightBg
	}

	if len(bgLines) > m.height {
		bgLines = bgLines[:m.height]
	}
	return strings.Join(bgLines, "\n")
}

// ---------- render node ----------

func renderNode(n node) string {
	connector := stDim.Render(n.connector)

	// Untracked branch in the detected tree
	if n.isUntracked {
		var name string
		if n.uIsCurrent {
			name = stCurrent.Render(n.uName)
		} else {
			name = stUntracked.Render(n.uName)
		}
		return connector + name
	}

	var name string
	if n.branch.Current {
		name = stCurrent.Render(n.branch.Name)
	} else {
		name = n.branch.Name
	}

	var ann []string

	if n.branch.Down != nil && n.branch.Down.NeedsRestack {
		ann = append(ann, stRestack.Render("⟳"))
	}

	if c := n.branch.Change; c != nil {
		var s lipgloss.Style
		switch c.Status {
		case "open":
			s = stPROpen
		case "merged":
			s = stPRMerged
		case "closed":
			s = stPRClosed
		default:
			s = stDim
		}
		ann = append(ann, s.Render("#"+c.ID))
	}

	if p := n.branch.Push; p != nil {
		if p.Ahead > 0 || p.Behind > 0 {
			ann = append(ann, stDim.Render(fmt.Sprintf("↑%d↓%d", p.Ahead, p.Behind)))
		} else if p.NeedsPush {
			ann = append(ann, stRestack.Render("↑"))
		}
	}

	line := connector + name
	if len(ann) > 0 {
		line += "  " + strings.Join(ann, " ")
	}
	return line
}

// ---------- helpers ----------

func padToSize(lines []string, height, width int) []string {
	for i, line := range lines {
		vis := lipgloss.Width(line)
		if vis < width {
			lines[i] = line + strings.Repeat(" ", width-vis)
		}
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

// truncLeft right-aligns s within max width, truncating from the left if needed.
func truncLeft(s string, max int) string {
	if len(s) <= max {
		return fmt.Sprintf("%*s", max, s)
	}
	return "…" + s[len(s)-max+1:]
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
