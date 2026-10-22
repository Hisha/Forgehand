package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Hisha/Forgehand/internal/ipc"
)

// accountCommand implements the local account administration commands.
//
// Every command travels over the local Unix socket and is authorized by the
// daemon through the peer credentials of the connection, so it is only
// reachable by the owner of the Forgehand installation.
func accountCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand account <bootstrap-admin|activate|list|grant|revoke|disable|revoke-sessions>")
	}

	switch args[0] {
	case "bootstrap-admin":
		return bootstrapAdministrator(args[1:])
	case "activate":
		return activateAccount(args[1:])
	case "list":
		return listAccounts(args[1:])
	case "grant":
		return grantAccountProject(args[1:])
	case "revoke":
		return revokeAccountProject(args[1:])
	case "disable":
		return disableAccount(args[1:])
	case "revoke-sessions":
		return revokeAccountSessions(args[1:])
	default:
		return fmt.Errorf("unknown account command: %s", args[0])
	}
}

// bootstrapAdministrator creates the first administrator account and prints
// the one-time enrollment material.
func bootstrapAdministrator(args []string) error {
	var username, displayName string

	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--username":
			if index+1 >= len(args) {
				return fmt.Errorf("missing value for --username")
			}
			username = args[index+1]
			index++
		case "--display-name":
			if index+1 >= len(args) {
				return fmt.Errorf("missing value for --display-name")
			}
			displayName = args[index+1]
			index++
		default:
			return fmt.Errorf("unknown flag: %s", args[index])
		}
	}

	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("--username is required")
	}

	password, err := readPassword("New password: ")
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:     "account-bootstrap-admin",
		Username:    username,
		DisplayName: displayName,
		Password:    password,
	})
	if err != nil {
		return err
	}

	if response.Account == nil || response.Enrollment == nil {
		return fmt.Errorf("daemon returned no enrollment material")
	}

	fmt.Printf("Created administrator %s (account %d)\n", response.Account.Username, response.Account.ID)
	fmt.Println()
	fmt.Println("Enrol this secret in an authenticator application now. It is shown once and is never stored in plaintext.")
	fmt.Printf("Secret: %s\n", response.Enrollment.Secret)
	fmt.Printf("Setup URI: %s\n", response.Enrollment.ProvisioningURI)
	fmt.Println()
	fmt.Println("Recovery codes (each is usable once):")
	for _, code := range response.Enrollment.RecoveryCodes {
		fmt.Printf("  %s\n", code)
	}
	fmt.Println()
	fmt.Println("Run `forgehand account activate --username " + response.Account.Username + "` to confirm enrollment with a code from your authenticator.")

	return nil
}

// activateAccount confirms one-time password enrollment and activates the
// account.
func activateAccount(args []string) error {
	username, err := singleUsernameArgument(args, "--username")
	if err != nil {
		return err
	}

	code, err := readSecret("One-time password from your authenticator: ")
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:  "account-activate",
		Username: username,
		TOTPCode: code,
	})
	if err != nil {
		return err
	}

	if response.Account != nil {
		fmt.Printf("Account %s is %s\n", response.Account.Username, response.Account.Status)
	}
	if response.Message != "" {
		fmt.Println(response.Message)
	}
	return nil
}

// listAccounts prints every known account without its secrets.
func listAccounts(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: forgehand account list")
	}

	response, err := request(ipc.Request{Command: "account-list"})
	if err != nil {
		return err
	}

	if len(response.Accounts) == 0 {
		fmt.Println("No accounts.")
		return nil
	}

	fmt.Println("ID  USERNAME  ROLE  STATUS  TOTP")
	for _, account := range response.Accounts {
		fmt.Printf(
			"%d  %s  %s  %s  %t\n",
			account.ID,
			account.Username,
			account.Role,
			account.Status,
			account.TOTPEnrolled,
		)
	}
	return nil
}

// grantAccountProject authorizes an account to access a project.
func grantAccountProject(args []string) error {
	username, projectID, err := usernameAndProjectArguments(args)
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:   "account-grant-project",
		Username:  username,
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	fmt.Println(response.Message)
	return nil
}

// revokeAccountProject removes an account's authorization for a project.
func revokeAccountProject(args []string) error {
	username, projectID, err := usernameAndProjectArguments(args)
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:   "account-revoke-project",
		Username:  username,
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	fmt.Println(response.Message)
	return nil
}

// disableAccount revokes an account and every session it owns.
func disableAccount(args []string) error {
	username, err := singleUsernameArgument(args, "--username")
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:  "account-disable",
		Username: username,
	})
	if err != nil {
		return err
	}
	fmt.Println(response.Message)
	return nil
}

// revokeAccountSessions invalidates every session of an account.
func revokeAccountSessions(args []string) error {
	username, err := singleUsernameArgument(args, "--username")
	if err != nil {
		return err
	}

	response, err := request(ipc.Request{
		Command:  "account-revoke-sessions",
		Username: username,
	})
	if err != nil {
		return err
	}
	fmt.Println(response.Message)
	return nil
}

// singleUsernameArgument extracts a username from a flag list.
func singleUsernameArgument(args []string, flag string) (string, error) {
	var username string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case flag:
			if index+1 >= len(args) {
				return "", fmt.Errorf("missing value for %s", flag)
			}
			username = args[index+1]
			index++
		default:
			return "", fmt.Errorf("unknown flag: %s", args[index])
		}
	}
	if strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("%s is required", flag)
	}
	return username, nil
}

// usernameAndProjectArguments parses the shared flags required by account
// commands that operate on both an account and a project. Both flags may be
// supplied in either order; missing values, unknown flags, duplicate flags,
// empty usernames, and invalid project IDs are rejected.
func usernameAndProjectArguments(args []string) (string, int64, error) {
	var username string
	usernameSet := false
	var projectID int64
	projectSet := false

	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--username":
			if index+1 >= len(args) {
				return "", 0, fmt.Errorf("missing value for --username")
			}
			if usernameSet {
				return "", 0, fmt.Errorf("duplicate flag: --username")
			}
			username = args[index+1]
			usernameSet = true
			index++
		case "--project":
			if index+1 >= len(args) {
				return "", 0, fmt.Errorf("missing value for --project")
			}
			if projectSet {
				return "", 0, fmt.Errorf("duplicate flag: --project")
			}
			parsed, err := strconv.ParseInt(args[index+1], 10, 64)
			if err != nil || parsed <= 0 {
				return "", 0, fmt.Errorf("invalid project ID: %s", args[index+1])
			}
			projectID = parsed
			projectSet = true
			index++
		default:
			return "", 0, fmt.Errorf("unknown flag: %s", args[index])
		}
	}

	if !usernameSet || strings.TrimSpace(username) == "" {
		return "", 0, fmt.Errorf("--username is required")
	}
	if !projectSet || projectID <= 0 {
		return "", 0, fmt.Errorf("--project is required")
	}
	return username, projectID, nil
}
