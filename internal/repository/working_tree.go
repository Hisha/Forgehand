package repository

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type WorkingTreeChange struct {
	Path         string
	OriginalPath string
	IndexStatus  string
	WorkStatus   string
	HeadBlob     string
	IndexBlob    string
	Untracked    bool
}

func WorkingTreeDelta(
	ctx context.Context,
	rootPath string,
) ([]WorkingTreeChange, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("repository root path must not be empty")
	}

	cmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		rootPath,
		"status",
		"--porcelain=v2",
		"-z",
		"--untracked-files=all",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect Git working tree: %w", err)
	}

	return parseWorkingTreeDelta(output)
}

func parseWorkingTreeDelta(
	data []byte,
) ([]WorkingTreeChange, error) {
	records := strings.Split(string(data), "\x00")
	changes := make([]WorkingTreeChange, 0, len(records))

	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}

		switch record[0] {
		case '?':
			if len(record) < 3 || record[1] != ' ' {
				return nil, fmt.Errorf(
					"invalid untracked Git status record %q",
					record,
				)
			}

			changes = append(changes, WorkingTreeChange{
				Path:      record[2:],
				Untracked: true,
			})

		case '1':
			change, err := parseOrdinaryWorkingTreeChange(record)
			if err != nil {
				return nil, err
			}

			changes = append(changes, change)

		case '2':
			if i+1 >= len(records) || records[i+1] == "" {
				return nil, fmt.Errorf(
					"renamed/copied Git status record missing original path",
				)
			}

			change, err := parseRenamedWorkingTreeChange(
				record,
				records[i+1],
			)
			if err != nil {
				return nil, err
			}

			changes = append(changes, change)
			i++

		case '!':
			// Ignored files are not requested by the status command,
			// but tolerate them if Git configuration causes one to appear.
			continue

		default:
			return nil, fmt.Errorf(
				"unsupported Git status record %q",
				record,
			)
		}
	}

	return changes, nil
}

func parseOrdinaryWorkingTreeChange(
	record string,
) (WorkingTreeChange, error) {
	fields := strings.SplitN(record, " ", 9)
	if len(fields) != 9 {
		return WorkingTreeChange{}, fmt.Errorf(
			"invalid ordinary Git status record %q",
			record,
		)
	}

	xy := fields[1]
	if len(xy) != 2 {
		return WorkingTreeChange{}, fmt.Errorf(
			"invalid Git status XY field %q",
			xy,
		)
	}

	return WorkingTreeChange{
		Path:        fields[8],
		IndexStatus: string(xy[0]),
		WorkStatus:  string(xy[1]),
		HeadBlob:    fields[6],
		IndexBlob:   fields[7],
	}, nil
}

func parseRenamedWorkingTreeChange(
	record string,
	originalPath string,
) (WorkingTreeChange, error) {
	fields := strings.SplitN(record, " ", 10)
	if len(fields) != 10 {
		return WorkingTreeChange{}, fmt.Errorf(
			"invalid renamed/copied Git status record %q",
			record,
		)
	}

	xy := fields[1]
	if len(xy) != 2 {
		return WorkingTreeChange{}, fmt.Errorf(
			"invalid Git status XY field %q",
			xy,
		)
	}

	return WorkingTreeChange{
		Path:         fields[9],
		OriginalPath: originalPath,
		IndexStatus:  string(xy[0]),
		WorkStatus:   string(xy[1]),
		HeadBlob:     fields[7],
		IndexBlob:    fields[8],
	}, nil
}
