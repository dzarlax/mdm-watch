package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── system_profiler JSON structures ──────────────────────────────────────────

type SPOutput struct {
	Data []SPCategory `json:"SPConfigurationProfileDataType"`
}

type SPCategory struct {
	Name  string            `json:"_name"`
	Items []json.RawMessage `json:"_items"`
}

type SPProfile struct {
	Name              string      `json:"_name"`
	Identifier        string      `json:"spconfigprofile_profile_identifier"`
	UUID              string      `json:"spconfigprofile_profile_uuid"`
	Organization      string      `json:"spconfigprofile_organization"`
	InstallDate       string      `json:"spconfigprofile_install_date"`
	InstallSource     string      `json:"spconfigprofile_install_source"`
	Description       string      `json:"spconfigprofile_description"`
	VerificationState string      `json:"spconfigprofile_verification_state"`
	RemovalDisallowed string      `json:"spconfigprofile_RemovalDisallowed"`
	Version           int         `json:"spconfigprofile_version"`
	Payloads          []SPPayload `json:"_items"`
}

type SPPayload struct {
	Name        string `json:"_name"`
	PayloadData string `json:"spconfigprofile_payload_data"`
}

type SPProvProfile struct {
	Name         string                 `json:"_name"`
	Description  string                 `json:"spconfigprofile_description"`
	Created      string                 `json:"provprofile_created"`
	Expires      string                 `json:"provprofile_expires"`
	TeamID       []string               `json:"provprofile_teamIdentifier"`
	Entitlements map[string]interface{} `json:"provprofile_entitlements"`
	Certs        []SPProvCert           `json:"provprofile_devCertificates"`
}

type SPProvCert struct {
	Name    string `json:"provprofile_cert_name"`
	Issuer  string `json:"provprofile_cert_issuer"`
	Expires string `json:"provprofile_cert_expires"`
}

// ── Minimal state stored between runs ────────────────────────────────────────

type ProfileRecord struct {
	UUID              string `json:"uuid"`
	Name              string `json:"name"`
	Org               string `json:"org"`
	InstallDate       string `json:"install_date"`
	Source            string `json:"source"`
	Category          string `json:"category"`
	RemovalDisallowed string `json:"removal_disallowed"`
}

type State struct {
	Profiles  map[string]ProfileRecord `json:"profiles"`
	LastCheck time.Time                `json:"last_check"`
}

// ── Paths ─────────────────────────────────────────────────────────────────────

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mdm-watch")
}

func statePath() string { return filepath.Join(stateDir(), "state.json") }
func logPath() string   { return filepath.Join(stateDir(), "changes.log") }

// ── State I/O ─────────────────────────────────────────────────────────────────

func loadState() State {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return State{Profiles: make(map[string]ProfileRecord)}
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{Profiles: make(map[string]ProfileRecord)}
	}
	if s.Profiles == nil {
		s.Profiles = make(map[string]ProfileRecord)
	}
	return s
}

func saveState(s State) {
	s.LastCheck = time.Now()
	if err := os.MkdirAll(stateDir(), 0700); err != nil {
		log.Printf("failed to create state dir: %v", err)
		return
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(statePath(), data, 0600); err != nil {
		log.Printf("failed to save state: %v", err)
	}
}

// ── Fetching ──────────────────────────────────────────────────────────────────

func runSystemProfiler() (*SPOutput, error) {
	out, err := exec.Command("system_profiler", "SPConfigurationProfileDataType", "-json").Output()
	if err != nil {
		return nil, fmt.Errorf("system_profiler: %w", err)
	}
	var sp SPOutput
	if err := json.Unmarshal(out, &sp); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return &sp, nil
}

func isProvCategory(name string) bool {
	return strings.Contains(name, "provprofile")
}

func isUserCategory(name string) bool {
	return strings.Contains(name, "User")
}

func profileKey(p SPProfile) string {
	if p.Identifier != "" {
		return p.Identifier
	}
	return p.UUID
}

func toRecords(sp *SPOutput) map[string]ProfileRecord {
	records := make(map[string]ProfileRecord)
	for _, cat := range sp.Data {
		if isProvCategory(cat.Name) {
			continue
		}
		label := "System"
		if isUserCategory(cat.Name) {
			label = "User"
		}
		for _, raw := range cat.Items {
			var p SPProfile
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			key := profileKey(p)
			if key == "" {
				continue
			}
			records[key] = ProfileRecord{
				UUID:              p.UUID,
				Name:              p.Name,
				Org:               p.Organization,
				InstallDate:       p.InstallDate,
				Source:            p.InstallSource,
				Category:          label,
				RemovalDisallowed: p.RemovalDisallowed,
			}
		}
	}
	return records
}

// ── Notifications + log ───────────────────────────────────────────────────────

const uiURL = "http://localhost:8765"

// notify sends a macOS notification. In serve mode the notification is clickable
// (opens uiURL) via terminal-notifier if available; falls back to osascript.
func notify(title, message string, clickable bool) {
	if clickable {
		// Prefer our native Swift helper (proper UNUserNotificationCenter)
		if mn, err := exec.LookPath("mdm-notifier"); err == nil {
			exec.Command(mn,
				"-title", title,
				"-message", message,
				"-open", uiURL,
			).Run()
			return
		}
		// Fallback: terminal-notifier
		if tn, err := exec.LookPath("terminal-notifier"); err == nil {
			exec.Command(tn,
				"-title", title,
				"-message", message,
				"-sound", "Basso",
				"-open", uiURL,
			).Run()
			return
		}
	}
	// Last resort: osascript (not clickable)
	script := fmt.Sprintf(`display notification %q with title %q sound name "Basso"`, message, title)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("notification failed: %v", err)
	}
}

func appendLog(entries []string) {
	if len(entries) == 0 {
		return
	}
	if err := os.MkdirAll(stateDir(), 0700); err != nil {
		log.Printf("failed to create state dir: %v", err)
		return
	}
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("failed to open log: %v", err)
		return
	}
	defer f.Close()
	ts := time.Now().Format(time.RFC3339)
	for _, e := range entries {
		fmt.Fprintf(f, "[%s] %s\n", ts, e)
	}
}

// ── Diff logic ────────────────────────────────────────────────────────────────

// runCheck fetches current profiles, diffs against saved state, notifies on changes.
// clickable=true when the HTTP server is running so the notification can link to it.
func runCheck(clickable bool) {
	sp, err := runSystemProfiler()
	if err != nil {
		log.Printf("fetch profiles: %v", err)
		return
	}
	current := toRecords(sp)
	prev := loadState()
	var entries []string

	for id, rec := range current {
		if _, exists := prev.Profiles[id]; !exists {
			msg := fmt.Sprintf("New profile: %s (org: %s, source: %s, category: %s)", rec.Name, rec.Org, rec.Source, rec.Category)
			notify("MDM Watch — New Profile", msg, clickable)
			entries = append(entries, "ADDED   "+msg)
			log.Println("ADDED:", msg)
		}
	}
	for id, rec := range prev.Profiles {
		if _, exists := current[id]; !exists {
			msg := fmt.Sprintf("Removed profile: %s (org: %s, source: %s, category: %s)", rec.Name, rec.Org, rec.Source, rec.Category)
			notify("MDM Watch — Profile Removed", msg, clickable)
			entries = append(entries, "REMOVED "+msg)
			log.Println("REMOVED:", msg)
		}
	}
	for id, cur := range current {
		if old, exists := prev.Profiles[id]; exists && old.UUID != cur.UUID {
			msg := fmt.Sprintf("Updated profile (UUID changed): %s (org: %s)", cur.Name, cur.Org)
			notify("MDM Watch — Profile Updated", msg, clickable)
			entries = append(entries, "UPDATED "+msg)
			log.Println("UPDATED:", msg)
		}
	}

	appendLog(entries)
	if len(entries) == 0 {
		log.Printf("No changes. %d profile(s) active.", len(current))
	}
	saveState(State{Profiles: current})
}

// ── Web UI data structures ────────────────────────────────────────────────────

// criticalPatterns — keywords in profile name/identifier/payload that indicate
// the profile has elevated access to user data, network traffic, or system behaviour.
var criticalPatterns = []string{
	"dlp", "data loss", "endpoint security", "network filter", "content filter",
	"web filter", "ssl inspection", "tls inspection", "traffic", "proxy",
	"keychain", "screen", "accessibility", "full disk access", "kernel extension",
	"system extension", "pppc", "privacy", "firewall", "vpn",
	"mdm agent", "mdm enrollment", "supervision",
}

func detectCritical(p UIProfile) (bool, []string) {
	combined := strings.ToLower(p.Name + " " + p.Identifier + " " + p.Description)
	for _, pl := range p.Payloads {
		combined += " " + strings.ToLower(pl.BundleID+" "+pl.Data)
	}
	var matched []string
	for _, kw := range criticalPatterns {
		if strings.Contains(combined, kw) {
			matched = append(matched, kw)
		}
	}
	return len(matched) > 0, matched
}

type UIProfile struct {
	Name              string
	Identifier        string
	UUID              string
	Org               string
	InstallDate       string
	Source            string
	Description       string
	RemovalDisallowed bool
	Verified          bool
	Critical          bool
	CriticalReasons   []string
	Payloads          []UIPayload
}

type UIPayload struct {
	BundleID string
	Data     string
}

type UIProvProfile struct {
	Name        string
	Description string
	Created     string
	Expires     string
	TeamID      string
	Certs       []string
	Entitlements []string
}

type LogEntry struct {
	Timestamp string
	Kind      string
	Message   string
}

type UIData struct {
	GeneratedAt  string
	System       []UIProfile
	User         []UIProfile
	Provisioning []UIProvProfile
	ChangeLog    []LogEntry
}

// ── Build UI data ─────────────────────────────────────────────────────────────

func buildUIData() (*UIData, error) {
	sp, err := runSystemProfiler()
	if err != nil {
		return nil, err
	}

	data := &UIData{GeneratedAt: time.Now().Format("2006-01-02 15:04:05")}

	for _, cat := range sp.Data {
		if isProvCategory(cat.Name) {
			for _, raw := range cat.Items {
				var p SPProvProfile
				if err := json.Unmarshal(raw, &p); err != nil {
					continue
				}
				uip := UIProvProfile{
					Name:        p.Name,
					Description: p.Description,
					Created:     formatTS(p.Created),
					Expires:     formatTS(p.Expires),
					TeamID:      strings.Join(p.TeamID, ", "),
				}
				for _, c := range p.Certs {
					uip.Certs = append(uip.Certs, fmt.Sprintf("%s (exp %s)", c.Name, formatTS(c.Expires)))
				}
				for k, v := range p.Entitlements {
					uip.Entitlements = append(uip.Entitlements, fmt.Sprintf("%s = %v", k, v))
				}
				sort.Strings(uip.Entitlements)
				data.Provisioning = append(data.Provisioning, uip)
			}
			sort.Slice(data.Provisioning, func(i, j int) bool {
				return data.Provisioning[i].Name < data.Provisioning[j].Name
			})
			continue
		}

		label := "System"
		if isUserCategory(cat.Name) {
			label = "User"
		}
		for _, raw := range cat.Items {
			var p SPProfile
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			uip := UIProfile{
				Name:              p.Name,
				Identifier:        p.Identifier,
				UUID:              p.UUID,
				Org:               p.Organization,
				InstallDate:       formatDate(p.InstallDate),
				Source:            p.InstallSource,
				Description:       p.Description,
				RemovalDisallowed: strings.EqualFold(p.RemovalDisallowed, "yes"),
				Verified:          strings.EqualFold(p.VerificationState, "verified"),
			}
			for _, pl := range p.Payloads {
				uip.Payloads = append(uip.Payloads, UIPayload{
					BundleID: pl.Name,
					Data:     strings.TrimSpace(pl.PayloadData),
				})
			}
			uip.Critical, uip.CriticalReasons = detectCritical(uip)
			if label == "System" {
				data.System = append(data.System, uip)
			} else {
				data.User = append(data.User, uip)
			}
		}
	}

	sort.Slice(data.System, func(i, j int) bool { return data.System[i].Name < data.System[j].Name })
	sort.Slice(data.User, func(i, j int) bool { return data.User[i].Name < data.User[j].Name })

	if raw, err := os.ReadFile(logPath()); err == nil {
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		for i := len(lines) - 1; i >= 0 && len(data.ChangeLog) < 50; i-- {
			if lines[i] != "" {
				data.ChangeLog = append(data.ChangeLog, parseLogLine(lines[i]))
			}
		}
	}

	return data, nil
}

func formatDate(s string) string {
	if i := strings.Index(s, "("); i != -1 {
		inner := s[i+1:]
		if j := strings.Index(inner, " "); j != -1 {
			return inner[:j]
		}
	}
	return s
}

func formatTS(s string) string {
	// "2026-03-29T20:11:02Z" → "2026-03-29"
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func parseLogLine(line string) LogEntry {
	entry := LogEntry{Message: line}
	if !strings.HasPrefix(line, "[") {
		return entry
	}
	end := strings.Index(line, "]")
	if end == -1 {
		return entry
	}
	entry.Timestamp = line[1:end]
	rest := strings.TrimSpace(line[end+1:])
	for _, kind := range []string{"ADDED", "REMOVED", "UPDATED"} {
		if strings.HasPrefix(rest, kind) {
			entry.Kind = kind
			entry.Message = strings.TrimSpace(rest[len(kind):])
			return entry
		}
	}
	entry.Message = rest
	return entry
}

// ── HTTP server ───────────────────────────────────────────────────────────────

const checkInterval = 5 * time.Minute

func serveUI(addr string) {
	tmpl := template.Must(template.New("ui").Parse(uiTemplate))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := buildUIData()
		if err != nil {
			http.Error(w, "Failed to fetch profiles: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("template: %v", err)
		}
	})

	// Initial check, then periodic
	go func() {
		runCheck(true)
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for range ticker.C {
			runCheck(true)
		}
	}()

	log.Printf("MDM Watch UI → http://%s", addr)
	exec.Command("open", "http://"+addr).Start()
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		addr := "localhost:8765"
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		serveUI(addr)
		return
	}
	// Standalone check (legacy / cron mode, no clickable notification)
	runCheck(false)
}

// ── HTML template ─────────────────────────────────────────────────────────────

const uiTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MDM Watch</title>
<style>
  :root {
    --bg: #f5f5f7;
    --card: #ffffff;
    --border: #e0e0e5;
    --text: #1d1d1f;
    --muted: #6e6e73;
    --mdm: #ff3b30;
    --verified: #34c759;
    --locked: #ff9500;
    --payload-bg: #f2f2f7;
    --code: #5856d6;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #1c1c1e;
      --card: #2c2c2e;
      --border: #3a3a3c;
      --text: #f5f5f7;
      --muted: #8e8e93;
      --payload-bg: #1c1c1e;
      --code: #bf5af2;
    }
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", sans-serif;
         background: var(--bg); color: var(--text); font-size: 14px; line-height: 1.5; }
  .container { max-width: 860px; margin: 0 auto; padding: 36px 20px; }

  header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 36px; }
  header h1 { font-size: 24px; font-weight: 700; letter-spacing: -0.5px; }
  .header-right { display: flex; align-items: center; gap: 16px; }
  .meta { color: var(--muted); font-size: 12px; }
  a.refresh { color: var(--muted); text-decoration: none; font-size: 13px;
              padding: 5px 12px; border: 1px solid var(--border); border-radius: 6px; }
  a.refresh:hover { background: var(--card); }

  section { margin-bottom: 36px; }
  .section-title { font-size: 12px; font-weight: 600; text-transform: uppercase;
                   letter-spacing: 0.6px; color: var(--muted); margin-bottom: 10px;
                   padding-bottom: 6px; border-bottom: 1px solid var(--border); }

  /* Config profile cards */
  .profile { background: var(--card); border: 1px solid var(--border); border-radius: 10px;
             margin-bottom: 8px; overflow: hidden; }
  .profile-header { display: grid;
                    grid-template-columns: 1fr auto auto;
                    align-items: center; gap: 12px;
                    padding: 12px 16px; cursor: pointer; }
  .profile-header:hover { background: rgba(128,128,128,0.05); }
  .profile-name { font-weight: 500; font-size: 14px; }
  .profile-org  { font-size: 12px; color: var(--muted); margin-top: 1px; }
  .badges { display: flex; gap: 5px; align-items: center; }
  .badge { font-size: 11px; font-weight: 500; padding: 2px 8px; border-radius: 20px;
           white-space: nowrap; }
  .badge-mdm      { background: rgba(255,59,48,0.1);   color: var(--mdm); }
  .badge-manual   { background: rgba(142,142,147,0.15); color: var(--muted); }
  .badge-locked   { background: rgba(255,149,0,0.1);   color: var(--locked); }
  .badge-ok       { background: rgba(52,199,89,0.1);   color: var(--verified); }
  .badge-critical { background: rgba(255,59,48,0.18);  color: var(--mdm);
                    font-weight: 700; border: 1px solid rgba(255,59,48,0.3); }
  .profile-date { font-size: 12px; color: var(--muted); white-space: nowrap; }

  .profile-body { border-top: 1px solid var(--border); padding: 14px 16px; display: none; }
  .profile-body.open { display: block; }
  .profile-desc { color: var(--muted); font-size: 13px; margin-bottom: 12px; }

  .meta-grid { display: grid; grid-template-columns: 110px 1fr; gap: 3px 10px;
               font-size: 12px; margin-bottom: 14px; }
  .meta-grid dt { color: var(--muted); }
  .meta-grid dd { font-family: "SF Mono", ui-monospace, monospace; word-break: break-all; font-size: 11px; }
  .critical-reasons { font-size: 11px; color: var(--mdm); margin-bottom: 12px;
                      background: rgba(255,59,48,0.07); border-radius: 6px; padding: 6px 10px; }

  .payloads-label { font-size: 11px; font-weight: 600; text-transform: uppercase;
                    letter-spacing: 0.4px; color: var(--muted); margin-bottom: 8px; }
  .payload { background: var(--payload-bg); border-radius: 7px; margin-bottom: 7px; overflow: hidden; }
  .payload-bundle { font-size: 12px; font-weight: 500; padding: 7px 12px;
                    font-family: "SF Mono", monospace; color: var(--code); }
  .payload-data { font-size: 11px; font-family: "SF Mono", monospace;
                  padding: 6px 12px 8px; border-top: 1px solid var(--border);
                  white-space: pre-wrap; word-break: break-word; color: var(--muted);
                  max-height: 300px; overflow-y: auto; }

  /* Provisioning profiles */
  .prov { background: var(--card); border: 1px solid var(--border); border-radius: 10px;
          margin-bottom: 8px; overflow: hidden; }
  .prov-header { display: grid; grid-template-columns: 1fr auto; align-items: center;
                 gap: 12px; padding: 10px 16px; cursor: pointer; }
  .prov-header:hover { background: rgba(128,128,128,0.05); }
  .prov-name { font-size: 13px; font-weight: 400; }
  .prov-meta { font-size: 11px; color: var(--muted); white-space: nowrap; }
  .prov-body { border-top: 1px solid var(--border); padding: 10px 16px; display: none; }
  .prov-body.open { display: block; }
  .ent-list { font-size: 11px; font-family: "SF Mono", monospace; color: var(--muted);
              padding: 6px 0; }
  .ent-list li { list-style: none; padding: 2px 0; }

  /* Change log */
  .changelog { background: var(--card); border: 1px solid var(--border); border-radius: 10px; overflow: hidden; }
  .changelog-empty { padding: 18px 16px; color: var(--muted); font-size: 13px; }
  .log-row { display: flex; gap: 12px; align-items: baseline;
             padding: 7px 16px; border-bottom: 1px solid var(--border); font-size: 12px; }
  .log-row:last-child { border-bottom: none; }
  .log-ts  { color: var(--muted); white-space: nowrap; font-family: "SF Mono", monospace; font-size: 11px; }
  .log-added   { color: var(--verified); font-weight: 600; min-width: 60px; }
  .log-removed { color: var(--mdm);      font-weight: 600; min-width: 60px; }
  .log-updated { color: var(--locked);   font-weight: 600; min-width: 60px; }
  .log-msg { flex: 1; }
</style>
</head>
<body>
<div class="container">

  <header>
    <h1>MDM Watch</h1>
    <div class="header-right">
      <span class="meta">{{.GeneratedAt}}</span>
      <a href="/" class="refresh">↻ Refresh</a>
    </div>
  </header>

  {{if .System}}
  <section>
    <div class="section-title">System profiles ({{len .System}})</div>
    {{range .System}}{{template "cfgprofile" .}}{{end}}
  </section>
  {{end}}

  {{if .User}}
  <section>
    <div class="section-title">User profiles ({{len .User}})</div>
    {{range .User}}{{template "cfgprofile" .}}{{end}}
  </section>
  {{end}}

  {{if .Provisioning}}
  <section>
    <div class="section-title">Provisioning profiles ({{len .Provisioning}}) — app signing</div>
    {{range .Provisioning}}{{template "provprofile" .}}{{end}}
  </section>
  {{end}}

  <section>
    <div class="section-title">Change log</div>
    <div class="changelog">
      {{if .ChangeLog}}
        {{range .ChangeLog}}
        <div class="log-row">
          <span class="log-ts">{{.Timestamp}}</span>
          {{if eq .Kind "ADDED"}}<span class="log-added">ADDED</span>
          {{else if eq .Kind "REMOVED"}}<span class="log-removed">REMOVED</span>
          {{else if eq .Kind "UPDATED"}}<span class="log-updated">UPDATED</span>
          {{else}}<span class="log-updated">CHANGE</span>{{end}}
          <span class="log-msg">{{.Message}}</span>
        </div>
        {{end}}
      {{else}}
        <div class="changelog-empty">No changes recorded yet.</div>
      {{end}}
    </div>
  </section>

</div>

{{define "cfgprofile"}}
<div class="profile">
  <div class="profile-header" onclick="toggle(this)">
    <div>
      <div class="profile-name">{{.Name}}</div>
      <div class="profile-org">{{.Org}}</div>
    </div>
    <div class="badges">
      {{if .Critical}}<span class="badge badge-critical" title="{{range .CriticalReasons}}{{.}} {{end}}">⚠ CRITICAL</span>{{end}}
      {{if .Verified}}<span class="badge badge-ok">Verified</span>{{end}}
      {{if .RemovalDisallowed}}<span class="badge badge-locked">Locked</span>{{end}}
      {{if eq .Source "MDM"}}<span class="badge badge-mdm">MDM</span>
      {{else if .Source}}<span class="badge badge-manual">{{.Source}}</span>{{end}}
    </div>
    <div class="profile-date">{{.InstallDate}}</div>
  </div>
  <div class="profile-body">
    {{if .Critical}}<div class="critical-reasons">⚠ Matches: {{range .CriticalReasons}}<strong>{{.}}</strong> {{end}}</div>{{end}}
    {{if .Description}}<div class="profile-desc">{{.Description}}</div>{{end}}
    <dl class="meta-grid">
      {{if .Identifier}}<dt>Identifier</dt><dd>{{.Identifier}}</dd>{{end}}
      <dt>UUID</dt><dd>{{.UUID}}</dd>
    </dl>
    {{if .Payloads}}
    <div class="payloads-label">Payloads ({{len .Payloads}})</div>
    {{range .Payloads}}
    <div class="payload">
      <div class="payload-bundle">{{.BundleID}}</div>
      {{if .Data}}<div class="payload-data">{{.Data}}</div>{{end}}
    </div>
    {{end}}
    {{end}}
  </div>
</div>
{{end}}

{{define "provprofile"}}
<div class="prov">
  <div class="prov-header" onclick="toggle(this)">
    <div class="prov-name">{{.Name}}</div>
    <div class="prov-meta">exp {{.Expires}} · {{.TeamID}}</div>
  </div>
  <div class="prov-body">
    {{if .Certs}}
    <div class="payloads-label" style="margin-bottom:4px">Certificates</div>
    <ul class="ent-list" style="margin-bottom:10px">
      {{range .Certs}}<li>{{.}}</li>{{end}}
    </ul>
    {{end}}
    {{if .Entitlements}}
    <div class="payloads-label" style="margin-bottom:4px">Entitlements</div>
    <ul class="ent-list">
      {{range .Entitlements}}<li>{{.}}</li>{{end}}
    </ul>
    {{end}}
  </div>
</div>
{{end}}

<script>
function toggle(el) { el.nextElementSibling.classList.toggle('open'); }
</script>
</body>
</html>`
