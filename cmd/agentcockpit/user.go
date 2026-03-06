package main

import (
	"github.com/spf13/cobra"
)

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage users (self-hosted admin only)",
}

var userListCmd = &cobra.Command{Use: "list", Short: "List all users", RunE: runUserList}
var userCreateCmd = &cobra.Command{Use: "create", Short: "Create a user", RunE: runUserCreate}
var userInviteCmd = &cobra.Command{Use: "invite", Short: "Generate an invite link", RunE: runUserInvite}
var userDeleteCmd = &cobra.Command{Use: "delete <email>", Short: "Delete a user", Args: cobra.ExactArgs(1), RunE: runUserDelete}

var (
	userEmail string
	userRole  string
)

func init() {
	userCreateCmd.Flags().StringVar(&userEmail, "email", "", "User email")
	userCreateCmd.Flags().StringVar(&userRole, "role", "user", "Role: user | admin")
	userInviteCmd.Flags().StringVar(&userEmail, "email", "", "Pre-fill email on signup page")
	userCmd.AddCommand(userListCmd, userCreateCmd, userInviteCmd, userDeleteCmd)
}

func runUserList(cmd *cobra.Command, args []string) error   { return nil }
func runUserCreate(cmd *cobra.Command, args []string) error { return nil }
func runUserInvite(cmd *cobra.Command, args []string) error { return nil }
func runUserDelete(cmd *cobra.Command, args []string) error { return nil }
