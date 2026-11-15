package host

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/room"
)

// DiscoverOpenCodeIn binds this pane's OpenCode server and session.
func DiscoverOpenCodeIn(getenv func(string) string, controlDir string) (string, string, error) {
	cwd, _ := os.Getwd()
	return discoverOpenCodeWithProcess(getenv, processCommandByPID, controlDir, cwd, defaultOpenCodeCandidates())
}

func discoverOpenCode(getenv func(string) string, controlDir, cwd string) (string, string, error) {
	return discoverOpenCodeWithProcess(getenv, processCommandByPID, controlDir, cwd, defaultOpenCodeCandidates())
}

func defaultOpenCodeCandidates() []string {
	ports := make([]int, 0, 15+16)
	for port := 4096; port <= 4110; port++ {
		ports = append(ports, port)
	}
	for port := 4200; port <= 4215; port++ {
		ports = append(ports, port)
	}
	candidates := make([]string, 0, len(ports))
	for _, port := range ports {
		candidates = append(candidates, "http://127.0.0.1:"+strconv.Itoa(port))
	}
	return candidates
}

func discoverOpenCodeWithCandidates(getenv func(string) string, controlDir, cwd string, candidates []string) (string, string, error) {
	return discoverOpenCodeWithProcess(getenv, processCommandByPID, controlDir, cwd, candidates)
}

func discoverOpenCodeWithProcess(getenv func(string) string, lookupCmd func(string) string, controlDir, cwd string, candidates []string) (string, string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if lookupCmd == nil {
		lookupCmd = processCommandByPID
	}
	if len(candidates) == 0 {
		candidates = defaultOpenCodeCandidates()
	}
	password := getenv("OPENCODE_SERVER_PASSWORD")
	paneURL, session, unservedTUI := PaneOpenCodeBindingWith(getenv, lookupCmd)
	if unservedTUI {
		return "", "", fmt.Errorf("this OpenCode window has no server for jkp to refresh; restart it with 'opencode --port 4096' (or another free port), 'opencode attach <url>', or ocmux")
	}
	bound, err := boundOpenCodeSessions(controlDir)
	if err != nil {
		return "", "", err
	}
	explicitURL := paneURL
	urls := make([]string, 0, len(candidates)+1)
	if explicitURL != "" {
		urls = append(urls, explicitURL)
	} else {
		if registryURL := ocmuxURLForCwd(cwd, getenv); registryURL != "" {
			urls = append(urls, registryURL)
		}
		urls = append(urls, candidates...)
	}
	urls = uniqueURLs(urls)
	cwdCanonical := canonicalPath(cwd)
	sawServer, sawOtherDirectory := false, false
	for _, candidate := range urls {
		pathInfo, ok := fetchOpenCodePath(candidate, password)
		if !ok {
			continue
		}
		sawServer = true
		if !pathServesCwd(pathInfo, cwdCanonical) {
			sawOtherDirectory = true
			if explicitURL != "" {
				return "", "", noServerForDirectory(cwd, pathInfo.Directory)
			}
			continue
		}
		if session != "" {
			sess, found := fetchOpenCodeSession(candidate, password, cwdCanonical, session)
			if !found || sess.ParentID != "" || (sess.Directory != "" && canonicalPath(sess.Directory) != cwdCanonical) {
				return "", "", fmt.Errorf("OpenCode session %s is not a top-level session for %s", session, cwdCanonical)
			}
			if bound[session] {
				return "", "", fmt.Errorf("OpenCode session %s is already bound to another jkp agent", session)
			}
			return candidate, session, nil
		}
		sessions, ok := fetchSessions(candidate, password, cwdCanonical)
		if !ok {
			if explicitURL != "" {
				return "", "", fmt.Errorf("cannot list OpenCode sessions at %s", candidate)
			}
			continue
		}
		if id := pickUnboundSession(sessions, bound, cwdCanonical); id != "" {
			return candidate, id, nil
		}
	}
	if sawOtherDirectory {
		return "", "", noServerForDirectory(cwd, "another directory")
	}
	if sawServer {
		return "", "", fmt.Errorf("no unbound top-level OpenCode session found for %s; open a session or pass --oc-session", cwdCanonical)
	}
	return "", "", noServerForDirectory(cwd, "")
}

type paneProcessInfo struct {
	SessionID   string
	ServerURL   string
	UnservedTUI bool
}

// processCommandByPID returns the process command line for the OpenCode pane
// identified by OPENCODE_PID. It is injectable so discovery tests never depend
// on the test runner's own process tree.
func processCommandByPID(pid string) string {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		return ""
	}
	output, err := exec.Command("ps", "-p", pid, "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func parseOpenCodeCommandLine(command string) paneProcessInfo {
	fields := strings.Fields(command)
	if len(fields) == 0 || !strings.HasPrefix(strings.ToLower(filepath.Base(fields[0])), "opencode") {
		return paneProcessInfo{}
	}

	info := paneProcessInfo{}
	hostname := "127.0.0.1"
	port := 0
	commandName := ""
	attach := false
	serverOnly := map[string]bool{
		"serve": true, "web": true, "run": true, "acp": true, "mcp": true,
		"completion": true, "debug": true, "providers": true, "auth": true,
		"agent": true, "upgrade": true, "uninstall": true, "models": true,
		"stats": true, "export": true, "import": true, "github": true,
		"pr": true, "session": true, "plugin": true, "plug": true, "db": true,
	}
	valueFlags := map[string]bool{
		"-s": true, "--session": true, "--port": true, "--hostname": true,
		"-p": true, "--password": true, "-u": true, "--username": true,
		"--log-level": true, "--replay-limit": true, "--dir": true,
	}
	for i := 1; i < len(fields); i++ {
		field := fields[i]
		if field == "attach" {
			commandName, attach = "attach", true
			continue
		}
		if field == "-s" || field == "--session" {
			if i+1 < len(fields) {
				i++
				info.SessionID = fields[i]
			}
			continue
		}
		if value, ok := strings.CutPrefix(field, "-s="); ok {
			info.SessionID = value
			continue
		}
		if value, ok := strings.CutPrefix(field, "--session="); ok {
			info.SessionID = value
			continue
		}
		if field == "--port" {
			if i+1 < len(fields) {
				i++
				port, _ = strconv.Atoi(fields[i])
			}
			continue
		}
		if value, ok := strings.CutPrefix(field, "--port="); ok {
			port, _ = strconv.Atoi(value)
			continue
		}
		if field == "--hostname" {
			if i+1 < len(fields) {
				i++
				hostname = fields[i]
			}
			continue
		}
		if value, ok := strings.CutPrefix(field, "--hostname="); ok {
			hostname = value
			continue
		}
		if valueFlags[field] {
			if i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			continue
		}
		if attach && info.ServerURL == "" {
			info.ServerURL = NormalizeOpenCodeURL(field)
			if info.ServerURL != "" {
				continue
			}
		}
		if commandName == "" && serverOnly[field] {
			commandName = field
		}
	}

	if info.ServerURL == "" && port > 0 {
		hostname = normalizeOpenCodeHostname(hostname)
		info.ServerURL = NormalizeOpenCodeURL("http://" + net.JoinHostPort(hostname, strconv.Itoa(port)))
	}
	info.UnservedTUI = info.ServerURL == "" && (commandName == "" || commandName == "attach")
	return info
}

// NormalizeOpenCodeURL normalizes local wildcard/localhost URLs to the loopback
// URL form used by the local server registry and agent bindings.
func NormalizeOpenCodeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return strings.TrimRight(raw, "/")
	}
	host := normalizeOpenCodeHostname(u.Hostname())
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/")
}

func normalizeOpenCodeHostname(host string) string {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return strings.Trim(host, "[]")
	}
}

// PaneOpenCodeBindingWith returns the pane's server URL, session ID, and
// whether it is an unserved TUI. Explicit environment overrides take precedence
// over process-derived values.
func PaneOpenCodeBindingWith(getenv func(string) string, lookupCmd func(string) string) (serverURL, sessionID string, unservedTUI bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if lookupCmd == nil {
		lookupCmd = processCommandByPID
	}
	info := paneProcessInfo{}
	if pid := strings.TrimSpace(getenv("OPENCODE_PID")); pid != "" {
		info = parseOpenCodeCommandLine(lookupCmd(pid))
	}
	serverURL = NormalizeOpenCodeURL(getenv("JKP_OC_URL"))
	if serverURL == "" {
		serverURL = info.ServerURL
	}
	for _, key := range []string{"JKP_OC_SESSION", "OPENCODE_SESSION_ID"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			sessionID = value
			break
		}
	}
	if sessionID == "" {
		sessionID = info.SessionID
	}
	unservedTUI = info.UnservedTUI && strings.TrimSpace(getenv("JKP_OC_URL")) == ""
	return serverURL, sessionID, unservedTUI
}

// PaneOpenCodeBinding returns the current pane's explicitly known server URL
// and session ID. It does not return an error for an unserved TUI; callers that
// initiate discovery should use PaneOpenCodeBindingWith to detect that case.
func PaneOpenCodeBinding(getenv func(string) string) (serverURL, sessionID string) {
	serverURL, sessionID, _ = PaneOpenCodeBindingWith(getenv, processCommandByPID)
	return serverURL, sessionID
}

func noServerForDirectory(cwd, other string) error {
	if other != "" {
		return fmt.Errorf("no OpenCode server is running for %s (found a server for %s); start OpenCode with 'opencode --port 4096' (or 'opencode serve --port 4096' + 'opencode attach', or ocmux), or set JKP_OC_URL", canonicalPath(cwd), canonicalPath(other))
	}
	return fmt.Errorf("no OpenCode server is running for %s; start OpenCode with 'opencode --port 4096' (or 'opencode serve --port 4096' + 'opencode attach', or ocmux), or set JKP_OC_URL", canonicalPath(cwd))
}

func uniqueURLs(urls []string) []string {
	seen := make(map[string]bool, len(urls))
	out := make([]string, 0, len(urls))
	for _, candidate := range urls {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}

func sessionIDFromCommandLine(command string) string {
	return parseOpenCodeCommandLine(command).SessionID
}

type ocmuxServe struct {
	Port int    `json:"port"`
	Cwd  string `json:"cwd"`
}

func ocmuxURLForCwd(cwd string, getenv func(string) string) string {
	if cwd == "" {
		return ""
	}
	cwdAbs := canonicalPath(cwd)
	if cwdAbs == "" {
		return ""
	}
	path := ""
	if getenv != nil {
		path = getenv("JKP_OCMUX_REGISTRY")
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".config", "ocmux", "serve-processes.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var serves []ocmuxServe
	if err := json.Unmarshal(data, &serves); err != nil {
		return ""
	}
	var best string
	bestLen := -1
	for _, serve := range serves {
		if serve.Port <= 0 || serve.Cwd == "" {
			continue
		}
		abs := canonicalPath(serve.Cwd)
		if abs == "" {
			continue
		}
		if cwdAbs == abs || strings.HasPrefix(cwdAbs, abs+string(filepath.Separator)) {
			if len(abs) > bestLen {
				bestLen = len(abs)
				best = fmt.Sprintf("http://127.0.0.1:%d", serve.Port)
			}
		}
	}
	return best
}

type ocSession struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Time      struct {
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type openCodePath struct {
	Directory string `json:"directory"`
	Worktree  string `json:"worktree"`
}

func fetchOpenCodePath(baseURL, password string) (openCodePath, bool) {
	var out openCodePath
	if err := openCodeGet(baseURL, password, "/path", "", &out); err != nil {
		return openCodePath{}, false
	}
	return out, true
}

func pathServesCwd(path openCodePath, cwd string) bool {
	for _, root := range []string{path.Directory, path.Worktree} {
		if root == "" {
			continue
		}
		root = canonicalPath(root)
		rel, err := filepath.Rel(root, cwd)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func fetchOpenCodeSession(baseURL, password, cwd, sessionID string) (ocSession, bool) {
	var out ocSession
	if err := openCodeGet(baseURL, password, "/session/"+url.PathEscape(sessionID), cwd, &out); err != nil {
		return ocSession{}, false
	}
	if out.ID == "" {
		out.ID = sessionID
	}
	return out, true
}

func openCodeGet(baseURL, password, path, directory string, out any) error {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return err
	}
	if directory != "" {
		query := u.Query()
		query.Set("directory", canonicalPath(directory))
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", password)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenCode GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func fetchSessions(baseURL, password string, directory ...string) ([]ocSession, bool) {
	endpoint := strings.TrimRight(baseURL, "/") + "/session"
	if len(directory) > 0 && directory[0] != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, false
		}
		query := parsed.Query()
		query.Set("directory", canonicalPath(directory[0]))
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
	}
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.SetBasicAuth("opencode", password)
	if req.Header.Get("Authorization") == "" {
		req.SetBasicAuth("opencode", "jkp-oc-token")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	var sessions []ocSession
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, false
	}
	return sessions, true
}

func pickUnboundSession(sessions []ocSession, bound map[string]bool, cwd string) string {
	var bestDir, bestUnknownDir, bestAny *ocSession
	cwdAbs := ""
	if cwd != "" {
		cwdAbs, _ = filepath.Abs(cwd)
	}
	for i := range sessions {
		session := &sessions[i]
		if session.ParentID != "" {
			continue
		}
		if bound[session.ID] {
			continue
		}
		if bestAny == nil || session.Time.Updated > bestAny.Time.Updated {
			bestAny = session
		}
		if cwdAbs == "" {
			continue
		}
		if session.Directory == "" {
			if bestUnknownDir == nil || session.Time.Updated > bestUnknownDir.Time.Updated {
				bestUnknownDir = session
			}
			continue
		}
		abs, err := filepath.Abs(session.Directory)
		if err != nil {
			continue
		}
		abs = canonicalPath(abs)
		if abs != canonicalPath(cwdAbs) {
			continue
		}
		if bestDir == nil || session.Time.Updated > bestDir.Time.Updated {
			bestDir = session
		}
	}
	if bestDir != nil {
		return bestDir.ID
	}
	if cwdAbs != "" {
		if bestUnknownDir != nil {
			return bestUnknownDir.ID
		}
		return ""
	}
	if bestAny == nil {
		return ""
	}
	return bestAny.ID
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

func boundOpenCodeSessions(controlDir string) (map[string]bool, error) {
	bound := make(map[string]bool)
	infos, err := control.LoadAgents(controlDir)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.OpenCodeURL != "" && info.SessionID != "" {
			bound[info.SessionID] = true
		}
	}
	return bound, nil
}

func reusableHostBinding(roomDir string) (string, string, bool) {
	r, err := room.Load(roomDir)
	if err != nil || !isCurrentRoom(r) || !control.IsProcessAlive(r.AgentPID) {
		return "", "", false
	}
	path, err := control.Path(roomDir, "host")
	if err != nil {
		return "", "", false
	}
	info, err := control.Load(path)
	if err != nil || !control.IsProcessAlive(info.PID) || info.OpenCodeURL == "" || info.SessionID == "" {
		return "", "", false
	}
	return info.OpenCodeURL, info.SessionID, true
}

func validateReusableHostBinding(info control.Info, requestedSession, cwd, password string) error {
	return validateReusableHostBindingForPane(info, "", requestedSession, cwd, password)
}

func validateReusableHostBindingForPane(info control.Info, requestedURL, requestedSession, cwd, password string) error {
	if requestedURL != "" && NormalizeOpenCodeURL(requestedURL) != NormalizeOpenCodeURL(info.OpenCodeURL) {
		return fmt.Errorf("a jkp host room is already bound to OpenCode session %s at %s; run jkp reset first to host from %s", info.SessionID, info.OpenCodeURL, requestedURL)
	}
	if requestedSession != "" && requestedSession != info.SessionID {
		return fmt.Errorf("a jkp host room is already bound to OpenCode session %s; run jkp reset first to host from session %s", info.SessionID, requestedSession)
	}
	pathInfo, ok := fetchOpenCodePath(info.OpenCodeURL, password)
	if !ok || !pathServesCwd(pathInfo, canonicalPath(cwd)) {
		boundDir := "an unavailable OpenCode directory"
		if pathInfo.Directory != "" {
			boundDir = pathInfo.Directory
		}
		return fmt.Errorf("a jkp host room is already bound to OpenCode session %s in %s; run jkp reset first to host from %s", info.SessionID, boundDir, canonicalPath(cwd))
	}
	session, ok := fetchOpenCodeSession(info.OpenCodeURL, password, canonicalPath(cwd), info.SessionID)
	if !ok {
		return fmt.Errorf("a jkp host room is already bound to OpenCode session %s, but that session cannot be verified in %s; run jkp reset first to host from this session", info.SessionID, canonicalPath(cwd))
	}
	if session.Directory != "" && canonicalPath(session.Directory) != canonicalPath(cwd) {
		return fmt.Errorf("a jkp host room is already bound to OpenCode session %s in %s; run jkp reset first to host from %s", info.SessionID, session.Directory, canonicalPath(cwd))
	}
	return nil
}

// OpenCodeSessionUpdated returns the server's update timestamp for sessionID.
func OpenCodeSessionUpdated(baseURL, password, sessionID string) (int64, bool) {
	if session, ok := fetchOpenCodeSession(baseURL, password, "", sessionID); ok {
		return session.Time.Updated, true
	}
	sessions, ok := fetchSessions(baseURL, password)
	if !ok {
		return 0, false
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return session.Time.Updated, true
		}
	}
	return 0, false
}
