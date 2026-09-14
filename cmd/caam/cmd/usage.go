package cmd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Display profile usage statistics",
	Long: `Display profile usage statistics based on caam's activity log.

Examples:
  caam usage
  caam usage --days 30
  caam usage --since 2025-12-01
  caam usage --profile claude/work --detailed
  caam usage --format json`,
	RunE: runUsage,
}

func init() {
	rootCmd.AddCommand(usageCmd)
	usageCmd.Flags().StringP("profile", "p", "", "profile to show (agent/name)")
	usageCmd.Flags().Bool("detailed", false, "show detailed session history (requires --profile)")
	usageCmd.Flags().Int("days", 7, "number of days to include")
	usageCmd.Flags().String("since", "", "start date (YYYY-MM-DD)")
	usageCmd.Flags().String("format", "table", "output format: table, json, csv")
}

func runUsage(cmd *cobra.Command, args []string) error {
	profileArg, _ := cmd.Flags().GetString("profile")
	detailed, _ := cmd.Flags().GetBool("detailed")
	days, _ := cmd.Flags().GetInt("days")
	sinceArg, _ := cmd.Flags().GetString("since")
	format, _ := cmd.Flags().GetString("format")

	since, err := parseSince(days, sinceArg)
	if err != nil {
		return err
	}

	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	out := cmd.OutOrStdout()

	if profileArg != "" {
		provider, profile, err := splitProviderProfile(profileArg)
		if err != nil {
			return err
		}

		if detailed {
			rows, err := querySessionHistory(db, provider, profile, since)
			if err != nil {
				return err
			}
			return renderUsageDetailed(out, format, provider, profile, since, rows)
		}

		summary, err := queryUsageSummary(db, since)
		if err != nil {
			return err
		}
		var filtered []usageSummaryRow
		for _, row := range summary {
			if row.Provider == provider && row.Profile == profile {
				filtered = append(filtered, row)
			}
		}
		return renderUsageSummary(out, format, since, filtered)
	}

	if detailed {
		return fmt.Errorf("--detailed requires --profile")
	}

	rows, err := queryUsageSummary(db, since)
	if err != nil {
		return err
	}
	return renderUsageSummary(out, format, since, rows)
}

type usageSummaryRow struct {
	Provider      string  `json:"provider"`
	Profile       string  `json:"profile"`
	Sessions      int     `json:"sessions"`
	ActiveSeconds int64   `json:"active_seconds"`
	ActiveHours   float64 `json:"active_hours"`
}

type sessionHistoryRow struct {
	Timestamp       time.Time `json:"timestamp"`
	DurationSeconds int64     `json:"duration_seconds"`
	DurationHours   float64   `json:"duration_hours"`
}

func queryUsageSummary(db *caamdb.DB, since time.Time) ([]usageSummaryRow, error) {
	if db == nil || db.Conn() == nil {
		return nil, fmt.Errorf("db not available")
	}

	rows, err := db.Conn().Query(
		`SELECT provider,
		        profile_name,
		        SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS sessions,
		        SUM(CASE WHEN event_type = ? THEN COALESCE(duration_seconds, 0) ELSE 0 END) AS active_seconds
		   FROM activity_log
		  WHERE datetime(timestamp) >= datetime(?)
		  GROUP BY provider, profile_name
		  ORDER BY active_seconds DESC, sessions DESC, provider ASC, profile_name ASC`,
		caamdb.EventActivate,
		caamdb.EventDeactivate,
		formatSQLiteSince(since),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage summary: %w", err)
	}
	defer rows.Close()

	var out []usageSummaryRow
	for rows.Next() {
		var row usageSummaryRow
		if err := rows.Scan(&row.Provider, &row.Profile, &row.Sessions, &row.ActiveSeconds); err != nil {
			return nil, fmt.Errorf("scan usage summary: %w", err)
		}
		row.ActiveHours = float64(row.ActiveSeconds) / 3600
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage summary: %w", err)
	}
	return out, nil
}

func querySessionHistory(db *caamdb.DB, provider, profile string, since time.Time) ([]sessionHistoryRow, error) {
	if db == nil || db.Conn() == nil {
		return nil, fmt.Errorf("db not available")
	}

	rows, err := db.Conn().Query(
		`SELECT timestamp, COALESCE(duration_seconds, 0)
		   FROM activity_log
		  WHERE provider = ? AND profile_name = ? AND event_type = ? AND datetime(timestamp) >= datetime(?)
		  ORDER BY datetime(timestamp) DESC`,
		provider,
		profile,
		caamdb.EventDeactivate,
		formatSQLiteSince(since),
	)
	if err != nil {
		return nil, fmt.Errorf("query session history: %w", err)
	}
	defer rows.Close()

	var out []sessionHistoryRow
	for rows.Next() {
		var tsStr string
		var seconds int64
		if err := rows.Scan(&tsStr, &seconds); err != nil {
			return nil, fmt.Errorf("scan session history: %w", err)
		}
		ts, err := parseSQLiteTime(tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp %q: %w", tsStr, err)
		}
		out = append(out, sessionHistoryRow{
			Timestamp:       ts,
			DurationSeconds: seconds,
			DurationHours:   float64(seconds) / 3600,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session history: %w", err)
	}
	return out, nil
}

func renderUsageSummary(w io.Writer, format string, since time.Time, rows []usageSummaryRow) error {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case "json":
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, string(data))
		return nil
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"provider", "profile", "sessions", "active_hours"})
		for _, r := range rows {
			_ = cw.Write([]string{
				r.Provider,
				r.Profile,
				fmt.Sprintf("%d", r.Sessions),
				fmt.Sprintf("%.1f", r.ActiveHours),
			})
		}
		cw.Flush()
		return cw.Error()
	case "table", "":
		if len(rows) == 0 {
			_, _ = fmt.Fprintln(w, "No usage data found.")
			return nil
		}

		_, _ = fmt.Fprintf(w, "Profile Usage (since %s)\n", since.Format("2006-01-02"))
		_, _ = fmt.Fprintln(w, "───────────────────────────────────────")

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "PROFILE\tSESSIONS\tHOURS")
		for _, r := range rows {
			_, _ = fmt.Fprintf(tw, "%s/%s\t%d\t%.1f\n", r.Provider, r.Profile, r.Sessions, r.ActiveHours)
		}
		_ = tw.Flush()
		return nil
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func renderUsageDetailed(w io.Writer, format, provider, profile string, since time.Time, rows []sessionHistoryRow) error {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case "json":
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, string(data))
		return nil
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"timestamp", "duration_hours"})
		for _, r := range rows {
			_ = cw.Write([]string{
				r.Timestamp.Format(time.RFC3339),
				fmt.Sprintf("%.3f", r.DurationHours),
			})
		}
		cw.Flush()
		return cw.Error()
	case "table", "":
		_, _ = fmt.Fprintf(w, "Session History for %s/%s (since %s)\n", provider, profile, since.Format("2006-01-02"))
		_, _ = fmt.Fprintln(w, "───────────────────────────────────────")

		if len(rows) == 0 {
			_, _ = fmt.Fprintln(w, "No sessions found.")
			return nil
		}

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "START\tDURATION")
		var totalSeconds int64
		for _, r := range rows {
			totalSeconds += r.DurationSeconds
			_, _ = fmt.Fprintf(tw, "%s\t%.2f hrs\n", r.Timestamp.Format("2006-01-02 15:04"), r.DurationHours)
		}
		_ = tw.Flush()

		totalHours := float64(totalSeconds) / 3600
		_, _ = fmt.Fprintf(w, "\nTotal: %d sessions, %.1f hours\n", len(rows), totalHours)
		return nil
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func parseSince(days int, since string) (time.Time, error) {
	if strings.TrimSpace(since) != "" {
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(since), time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse --since: %w", err)
		}
		return t.UTC(), nil
	}

	if days <= 0 {
		days = 7
	}
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

func splitProviderProfile(input string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(input), "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("profile must be in agent/name format")
	}
	provider := strings.TrimSpace(parts[0])
	profile := strings.TrimSpace(parts[1])
	if provider == "" || profile == "" {
		return "", "", fmt.Errorf("profile must be in agent/name format")
	}
	return provider, profile, nil
}

func formatSQLiteSince(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01 00:00:00"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func parseSQLiteTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if ts, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
		return ts, nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format")
}
