package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func investigateCommand(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: forgehand investigate <project-id> --component <component-identity> [--analysis <id>] [--format text|json] [--limit <n>]")
	}

	projectID, err := parseQueryID(args[0], "project ID")
	if err != nil {
		return err
	}

	req := ipc.Request{Command: "investigate", ProjectID: projectID}
	req.Identity = ""
	req.ComponentPath = ""
	req.Format = "text"
	req.Limit = 0

	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--component":
			value, next, err := queryFlagValue(args, index, "--component")
			if err != nil {
				return err
			}
			req.Identity = value
			index = next
		case "--analysis":
			value, next, err := queryFlagValue(args, index, "--analysis")
			if err != nil {
				return err
			}
			id, err := parseQueryID(value, "analysis ID")
			if err != nil {
				return err
			}
			req.AnalysisID = id
			req.Analysis = id
			index = next
		case "--format":
			value, next, err := queryFlagValue(args, index, "--format")
			if err != nil {
				return err
			}
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "text" && value != "json" {
				return fmt.Errorf("invalid format: %s (use text or json)", value)
			}
			req.Format = value
			index = next
		case "--limit":
			value, next, err := queryFlagValue(args, index, "--limit")
			if err != nil {
				return err
			}
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 0 {
				return fmt.Errorf("invalid limit: %s", value)
			}
			req.Limit = limit
			index = next
		default:
			return fmt.Errorf("unknown investigate option: %s", args[index])
		}
	}

	if req.Identity == "" {
		return fmt.Errorf("--component is required")
	}

	return sendInvestigation(req)
}

func sendInvestigation(req ipc.Request) error {
	response, err := request(req)
	if err != nil {
		return err
	}
	if response.Investigation == nil {
		return fmt.Errorf("daemon returned no investigation result")
	}
	inv := *response.Investigation

	if req.Format == "json" || strings.ToLower(strings.TrimSpace(req.Format)) == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(inv); err != nil {
			return err
		}
		// write warnings to stderr
		if len(inv.Warnings) > 0 {
			for _, w := range inv.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}
		}
		return nil
	}

	var output strings.Builder
	fmt.Fprintf(&output, "Investigation of component %s (project %d)\n", inv.Component.Identity, inv.ProjectID)
	fmt.Fprintf(&output, "Analysis: %d\n", inv.AnalysisID)
	fmt.Fprintf(&output, "Commit: %s\n", inv.Commit)
	fmt.Fprintf(&output, "Status: %s\n", inv.Status)
	if inv.Complete {
		fmt.Fprintln(&output, "Complete: yes")
	} else {
		fmt.Fprintln(&output, "Complete: no (partial)")
	}
	fmt.Fprintf(&output, "Component: %s (kind=%s, path=%s, internal=%t)\n", inv.Component.Identity, inv.Component.Kind, inv.Component.Path, inv.Component.Internal)
	fmt.Fprintf(&output, "Dependencies (outgoing): %d\n", len(inv.Dependencies))
	for _, d := range inv.Dependencies {
		fmt.Fprintf(&output, "  %s -> %s [%s]\n", d.Source, d.Target, d.Kind)
	}
	fmt.Fprintf(&output, "Dependents (incoming): %d\n", len(inv.Dependents))
	for _, d := range inv.Dependents {
		fmt.Fprintf(&output, "  %s -> %s [%s]\n", d.Source, d.Target, d.Kind)
	}
	fmt.Fprintf(&output, "Evidence entries: %d\n", len(inv.Evidence))
	for _, e := range inv.Evidence {
		fmt.Fprintf(&output, "  %s:%d %s (blob=%s)\n", e.Path, e.Line, sanitizeTableCell(e.Declaration), e.BlobHash)
	}
	fmt.Fprintf(&output, "Suggested investigation files: %d\n", len(inv.SuggestedFiles))
	for _, f := range inv.SuggestedFiles {
		reasons := strings.Join(f.Reasons, ",")
		fmt.Fprintf(&output, "  %s [%s]\n", f.Path, reasons)
	}
	if inv.Truncated {
		fmt.Fprintf(&output, "Note: output truncated (by %s)\n", inv.TruncatedBy)
	}
	if len(inv.Warnings) > 0 {
		fmt.Fprintln(&output, "Warnings:")
		for _, w := range inv.Warnings {
			fmt.Fprintf(&output, "  - %s\n", w)
		}
	}
	fmt.Fprintln(&output, "Investigation reports committed observations only; no builds or tests were run.")

	return writeTabularOutput(os.Stdout, output.String())
}
