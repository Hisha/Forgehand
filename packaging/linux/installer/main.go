package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	user := fs.String("user", "", "Service account name (explicit)")
	createUser := fs.Bool("create-user", false, "Create dedicated service account if requested")
	source := fs.String("source", "", "Path to forgehand binary")
	assumeYes := fs.Bool("yes", false, "Non-interactive")
	help := fs.Bool("help", false, "Show help")
	fs.BoolVar(help, "h", false, "Show help")

	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *help {
		usageCmd(cmd)
		os.Exit(0)
	}

	cfg := installConfig{
		User:       *user,
		CreateUser: *createUser,
		Source:     *source,
		AssumeYes:  *assumeYes,
	}

	o := newOps(realRunner{})

	switch cmd {
	case "install":
		if err := doInstall(cfg, o); err != nil {
			fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("install completed")
		os.Exit(0)
	case "upgrade":
		if err := doUpgrade(cfg, o); err != nil {
			fmt.Fprintf(os.Stderr, "upgrade failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("upgrade completed")
		os.Exit(0)
	case "status":
		if err := doStatus(cfg, o); err != nil {
			fmt.Fprintf(os.Stderr, "status check failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("status ok")
		os.Exit(0)
	case "uninstall":
		if err := doUninstall(cfg, o); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("uninstall completed")
		os.Exit(0)
	case "help", "--help", "-h":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("forgehand-installer")
	fmt.Println("Usage: install.sh <install|upgrade|status|uninstall> [options]")
	fmt.Println("  --user <name>        Service account name (explicit)")
	fmt.Println("  --create-user        Create dedicated service account if requested")
	fmt.Println("  --source <path>      Path to forgehand binary")
	fmt.Println("  --yes                Non-interactive")
	fmt.Println("  --help               Show help")
}

func usageCmd(cmd string) {
	usage()
}
