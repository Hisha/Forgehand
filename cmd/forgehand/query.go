package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func queryCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand query <components|dependencies|dependents|evidence|changes> ...")
	}

	switch args[0] {
	case "components":
		return queryComponents(args[1:])
	case "dependencies":
		return queryDependencies(args[1:])
	case "dependents":
		return queryDependents(args[1:])
	case "evidence":
		return queryEvidence(args[1:])
	case "changes":
		return queryChanges(args[1:])
	default:
		return fmt.Errorf("unknown query command: %s", args[0])
	}
}

func queryComponents(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand query components <id> [--kind K] [--identity I] [--path P] [--contains S] [--analysis N] [--limit N] [--evidence]")
	}

	projectID, err := parseQueryID(args[0], "project ID")
	if err != nil {
		return err
	}

	req := ipc.Request{Command: "query-components", ProjectID: projectID}
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--kind":
			value, next, err := queryFlagValue(args, index, "--kind")
			if err != nil {
				return err
			}
			req.Kind = value
			index = next
		case "--identity":
			value, next, err := queryFlagValue(args, index, "--identity")
			if err != nil {
				return err
			}
			req.Identity = value
			index = next
		case "--path":
			value, next, err := queryFlagValue(args, index, "--path")
			if err != nil {
				return err
			}
			req.ComponentPath = value
			index = next
		case "--contains":
			value, next, err := queryFlagValue(args, index, "--contains")
			if err != nil {
				return err
			}
			req.Contains = value
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
		case "--evidence":
			req.IncludeEvidence = true
		default:
			return fmt.Errorf("unknown components option: %s", args[index])
		}
	}

	return sendQuery(req)
}

func queryDependencies(args []string) error {
	return queryByIdentity(args, "query-dependencies", "dependencies")
}

func queryDependents(args []string) error {
	return queryByIdentity(args, "query-dependents", "dependents")
}

func queryEvidence(args []string) error {
	return queryByIdentity(args, "query-evidence", "evidence")
}

func queryByIdentity(args []string, command, label string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: forgehand query %s <id> <component-identity> [--analysis N] [--evidence]", label)
	}

	projectID, err := parseQueryID(args[0], "project ID")
	if err != nil {
		return err
	}

	req := ipc.Request{Command: command, ProjectID: projectID, Identity: args[1]}
	for index := 2; index < len(args); index++ {
		switch args[index] {
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
			index = next
		case "--evidence":
			req.IncludeEvidence = true
		default:
			return fmt.Errorf("unknown %s option: %s", label, args[index])
		}
	}

	return sendQuery(req)
}

func queryChanges(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand query changes <id> --from <analysis-id> --to <analysis-id>")
	}

	projectID, err := parseQueryID(args[0], "project ID")
	if err != nil {
		return err
	}

	req := ipc.Request{Command: "query-changes", ProjectID: projectID}
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--from":
			value, next, err := queryFlagValue(args, index, "--from")
			if err != nil {
				return err
			}
			id, err := parseQueryID(value, "from analysis ID")
			if err != nil {
				return err
			}
			req.FromAnalysisID = id
			index = next
		case "--to":
			value, next, err := queryFlagValue(args, index, "--to")
			if err != nil {
				return err
			}
			id, err := parseQueryID(value, "to analysis ID")
			if err != nil {
				return err
			}
			req.ToAnalysisID = id
			index = next
		default:
			return fmt.Errorf("unknown changes option: %s", args[index])
		}
	}

	if req.FromAnalysisID == 0 || req.ToAnalysisID == 0 {
		return fmt.Errorf("query changes requires --from <analysis-id> and --to <analysis-id>")
	}

	return sendQuery(req)
}

func sendQuery(req ipc.Request) error {
	response, err := request(req)
	if err != nil {
		return err
	}
	if response.Query == nil {
		return fmt.Errorf("daemon returned no query result")
	}
	return formatQueryResponse(os.Stdout, *response.Query)
}

func formatQueryResponse(writer io.Writer, query ipc.QueryResponse) error {
	var output strings.Builder

	fmt.Fprintf(&output, "Queried project %d %s\n", query.ProjectID, query.Query)

	if query.Observation != nil {
		appendQueryObservation(&output, *query.Observation)
	}
	if query.Changes != nil {
		appendQueryComparison(&output, *query.Changes)
	}

	switch query.Query {
	case "components":
		appendQueryComponents(&output, query)
	case "dependencies":
		appendQueryDependencies(&output, query)
	case "dependents":
		appendQueryDependents(&output, query)
	case "evidence":
		appendQueryEvidence(&output, query)
	}

	if query.Limited {
		fmt.Fprintln(&output, "Note: output truncated by --limit.")
	}
	if len(query.Warnings) > 0 {
		fmt.Fprintln(&output, "Warnings:")
		for _, warning := range query.Warnings {
			fmt.Fprintf(&output, "  - %s\n", warning)
		}
	}

	fmt.Fprintln(&output, "Query reports committed observations only; no builds or tests were run.")

	return writeTabularOutput(writer, output.String())
}

func writeTabularOutput(writer io.Writer, output string) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := io.WriteString(table, output); err != nil {
		return err
	}
	return table.Flush()
}

func sanitizeTableCell(value string) string {
	replacer := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	return replacer.Replace(value)
}

func appendQueryObservation(output *strings.Builder, observation ipc.QueryObservation) {
	fmt.Fprintf(output, "Analysis: %d\n", observation.AnalysisID)
	fmt.Fprintf(output, "Commit: %s\n", observation.Commit)
	switch observation.Status {
	case "current":
		fmt.Fprintf(output, "Status: current (HEAD %s)\n", observation.Head)
	case "stale":
		fmt.Fprintf(output, "Status: stale (HEAD %s)\n", observation.Head)
	default:
		fmt.Fprintln(output, "Status: unavailable")
	}
	if observation.Complete {
		fmt.Fprintln(output, "Complete: yes")
	} else {
		fmt.Fprintf(
			output,
			"Complete: no (partial; %d of %d source(s) skipped)\n",
			observation.Skipped,
			observation.Sources,
		)
	}
}

func appendQueryComponents(output *strings.Builder, query ipc.QueryResponse) {
	components := query.Components
	fmt.Fprintf(output, "Components: %d\n", len(components))
	if len(components) == 0 {
		fmt.Fprintln(output, "No components matched.")
		return
	}
	fmt.Fprintln(output, "KIND\tIDENTITY\tPATH\tINTERNAL")
	for _, component := range components {
		fmt.Fprintf(
			output,
			"%s\t%s\t%s\t%t\n",
			component.Kind,
			component.Identity,
			component.Path,
			component.Internal,
		)
	}
	appendComponentEvidence(output, components)
}

func appendQueryDependencies(output *strings.Builder, query ipc.QueryResponse) {
	if len(query.Components) > 0 {
		source := query.Components[0]
		fmt.Fprintf(output, "Source: %s (%s)\n", source.Identity, source.Kind)
	}
	dependencies := query.Dependencies
	fmt.Fprintf(output, "Dependencies: %d\n", len(dependencies))
	if len(dependencies) == 0 {
		fmt.Fprintln(output, "No outgoing dependencies.")
		return
	}
	fmt.Fprintln(output, "KIND\tSOURCE\tTARGET\tMODULE")
	for _, dependency := range dependencies {
		fmt.Fprintf(
			output,
			"%s\t%s\t%s\t%s\n",
			dependency.Kind,
			dependency.Source,
			dependency.Target,
			dependency.TargetModule,
		)
	}
	appendDependencyEvidence(output, dependencies)
}

func appendQueryDependents(output *strings.Builder, query ipc.QueryResponse) {
	if len(query.Components) > 0 {
		target := query.Components[0]
		fmt.Fprintf(output, "Component: %s (%s)\n", target.Identity, target.Kind)
	}
	dependents := query.Dependents
	fmt.Fprintf(output, "Dependents: %d\n", len(dependents))
	if len(dependents) == 0 {
		fmt.Fprintln(output, "No direct dependents.")
		return
	}
	fmt.Fprintln(output, "KIND\tSOURCE\tTARGET")
	for _, dependent := range dependents {
		fmt.Fprintf(output, "%s\t%s\t%s\n", dependent.Kind, dependent.Source, dependent.Target)
	}
	if len(dependents) > 0 && len(dependents[0].Evidence) > 0 {
		fmt.Fprintln(output, "Dependent evidence:")
		fmt.Fprintln(output, "SOURCE\tTARGET\tPATH\tLINE\tDECLARATION\tBLOB")
		for _, dependent := range dependents {
			for _, evidence := range dependent.Evidence {
				fmt.Fprintf(
					output,
					"%s\t%s\t%s\t%d\t%s\t%s\n",
					dependent.Source,
					dependent.Target,
					evidence.Path,
					evidence.Line,
					sanitizeTableCell(evidence.Declaration),
					evidence.BlobHash,
				)
			}
		}
	}
}

func appendQueryEvidence(output *strings.Builder, query ipc.QueryResponse) {
	identity := ""
	kind := ""
	if len(query.Components) > 0 {
		identity = query.Components[0].Identity
		kind = query.Components[0].Kind
	}
	fmt.Fprintf(output, "Component: %s (%s)\n", identity, kind)
	evidence := query.Evidence
	fmt.Fprintf(output, "Evidence: %d\n", len(evidence))
	if len(evidence) == 0 {
		fmt.Fprintln(output, "No evidence recorded.")
		return
	}
	fmt.Fprintln(output, "PATH\tLINE\tDECLARATION\tBLOB")
	for _, item := range evidence {
		fmt.Fprintf(output, "%s\t%d\t%s\t%s\n", item.Path, item.Line, sanitizeTableCell(item.Declaration), item.BlobHash)
	}
}

func appendQueryComparison(output *strings.Builder, changes ipc.QueryChanges) {
	fmt.Fprintf(output, "From analysis: %d (status %s, commit %s)\n", changes.From.AnalysisID, changes.From.Status, changes.From.Commit)
	fmt.Fprintf(output, "To analysis: %d (status %s, commit %s)\n", changes.To.AnalysisID, changes.To.Status, changes.To.Commit)

	fmt.Fprintf(output, "Added components: %d\n", len(changes.AddedComponents))
	for _, component := range changes.AddedComponents {
		fmt.Fprintf(output, "  %s\t%s\n", component.Kind, component.Identity)
	}
	fmt.Fprintf(output, "Removed components: %d\n", len(changes.RemovedComponents))
	for _, component := range changes.RemovedComponents {
		fmt.Fprintf(output, "  %s\t%s\n", component.Kind, component.Identity)
	}
	fmt.Fprintf(output, "Added dependencies: %d\n", len(changes.AddedDependencies))
	for _, dependency := range changes.AddedDependencies {
		fmt.Fprintf(output, "  %s\t%s\t%s\n", dependency.Kind, dependency.Source, dependency.Target)
	}
	fmt.Fprintf(output, "Removed dependencies: %d\n", len(changes.RemovedDependencies))
	for _, dependency := range changes.RemovedDependencies {
		fmt.Fprintf(output, "  %s\t%s\t%s\n", dependency.Kind, dependency.Source, dependency.Target)
	}
	fmt.Fprintf(output, "Reclassified dependencies: %d\n", len(changes.Reclassified))
	for _, reclassified := range changes.Reclassified {
		fmt.Fprintf(
			output,
			"  %s\t%s\t%s -> %s\n",
			reclassified.Source,
			reclassified.Target,
			reclassified.FromKind,
			reclassified.ToKind,
		)
	}
}

func appendComponentEvidence(output *strings.Builder, components []ipc.AnalysisComponent) {
	hasEvidence := false
	for _, component := range components {
		if len(component.Evidence) > 0 {
			hasEvidence = true
			break
		}
	}
	if !hasEvidence {
		return
	}
	fmt.Fprintln(output, "Component evidence:")
	fmt.Fprintln(output, "IDENTITY\tPATH\tLINE\tDECLARATION\tBLOB")
	for _, component := range components {
		for _, evidence := range component.Evidence {
			fmt.Fprintf(
				output,
				"%s\t%s\t%d\t%s\t%s\n",
				component.Identity,
				evidence.Path,
				evidence.Line,
				sanitizeTableCell(evidence.Declaration),
				evidence.BlobHash,
			)
		}
	}
}

func appendDependencyEvidence(output *strings.Builder, dependencies []ipc.AnalysisDependency) {
	hasEvidence := false
	for _, dependency := range dependencies {
		if len(dependency.Evidence) > 0 {
			hasEvidence = true
			break
		}
	}
	if !hasEvidence {
		return
	}
	fmt.Fprintln(output, "Dependency evidence:")
	fmt.Fprintln(output, "SOURCE\tTARGET\tPATH\tLINE\tDECLARATION\tBLOB")
	for _, dependency := range dependencies {
		for _, evidence := range dependency.Evidence {
			fmt.Fprintf(
				output,
				"%s\t%s\t%s\t%d\t%s\t%s\n",
				dependency.Source,
				dependency.Target,
				evidence.Path,
				evidence.Line,
				sanitizeTableCell(evidence.Declaration),
				evidence.BlobHash,
			)
		}
	}
}

func queryFlagValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s requires a value", flag)
	}
	return args[index+1], index + 1, nil
}

func parseQueryID(value, label string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s: %s", label, value)
	}
	return id, nil
}
