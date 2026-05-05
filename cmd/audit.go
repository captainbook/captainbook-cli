package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/spf13/cobra"
)

// auditCmd is the parent for `ceebee audit list` / `ceebee audit show`.
//
// Both subcommands default to --format json: this audit log is mostly
// consumed by Claude Code parsing forensics, and JSON is the contract.
// `--format table` is supported for human triage when something goes
// sideways at 2 AM.
func auditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect the local mutation audit log",
		Long: "Read and search ~/.ceebee/audit.jsonl, the local record of every\n" +
			"cli:write / cli:cs mutation issued by ceebee. The log is append-only,\n" +
			"size-rotated at 50 MB, and protected by a file lock so concurrent\n" +
			"ceebee processes don't interleave entries.",
	}

	cmd.AddCommand(auditListCmd())
	cmd.AddCommand(auditShowCmd())
	return cmd
}

func auditListCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent audit log entries (most recent first)",
		Long: "Print up to --limit entries from ~/.ceebee/audit.jsonl in\n" +
			"newest-first order. Reads transparently across rotated files\n" +
			"(audit.jsonl + audit.jsonl.1 ... audit.jsonl.3).",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := inventory.DefaultAuditPath()
			if err != nil {
				return err
			}
			r, err := inventory.NewReader(path)
			if err != nil {
				return err
			}
			entries, err := r.List(limit)
			if err != nil {
				return fmt.Errorf("reading audit log: %w", err)
			}
			return renderAuditEntries(entries, formatFlag)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of entries to return (0 = no limit)")
	return cmd
}

func auditShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <idempotency-key>",
		Short: "Show the audit entry for a specific idempotency key",
		Long: "Look up a single mutation by its idempotency key, searching the\n" +
			"active log and all rotated files. Exits non-zero if the key is\n" +
			"not found.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := inventory.DefaultAuditPath()
			if err != nil {
				return err
			}
			r, err := inventory.NewReader(path)
			if err != nil {
				return err
			}
			entry, err := r.Show(args[0])
			if err != nil {
				if errors.Is(err, inventory.ErrNotFound) {
					return fmt.Errorf("audit: no entry with idempotency_key %q", args[0])
				}
				return fmt.Errorf("reading audit log: %w", err)
			}
			return renderAuditEntries([]inventory.AuditEntry{*entry}, formatFlag)
		},
	}
	return cmd
}

// renderAuditEntries writes entries to stdout in the requested format.
// Supports json (default) and table; csv would be possible but is omitted
// because forensic_summary is dynamically shaped and doesn't fit a flat
// columnar layout cleanly.
func renderAuditEntries(entries []inventory.AuditEntry, format string) error {
	switch format {
	case "", "json":
		return renderAuditJSON(entries)
	case "table":
		return renderAuditTable(entries)
	default:
		return fmt.Errorf("unknown format %q (use json or table)", format)
	}
}

func renderAuditJSON(entries []inventory.AuditEntry) error {
	// Emit a JSON array so jq-style consumers can stream cleanly. For a
	// single entry (audit show), still wrap in an array — uniform shape
	// is more important than saving two characters.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func renderAuditTable(entries []inventory.AuditEntry) error {
	if len(entries) == 0 {
		fmt.Println("(no audit entries)")
		return nil
	}
	// Lightweight, dependency-free rendering. tablewriter would give us
	// fancier borders but the consumer is a human reading stderr output,
	// not a tool — minimal markup is friendlier to grep.
	headers := []string{"TS", "COMMAND", "STATUS", "ABILITY", "DRY_RUN", "IDEMPOTENCY_KEY"}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []string{
			e.Ts.Format("2006-01-02T15:04:05Z"),
			e.Command,
			fmt.Sprintf("%d", e.Status),
			e.AbilityUsed,
			fmt.Sprintf("%t", e.DryRun),
			e.IdempotencyKey,
		})
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	printRow := func(cells []string) {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = padRight(c, widths[i])
		}
		fmt.Println(strings.Join(parts, "  "))
	}
	printRow(headers)
	for _, r := range rows {
		printRow(r)
	}
	return nil
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
