package auth

import "time"

type RoleName string

const (
	RoleAdmin     RoleName = "admin"
	RoleOperator  RoleName = "operator"
	RoleDeveloper RoleName = "developer"
	RoleViewer    RoleName = "viewer"
)

type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Enabled      bool
	CreatedAt    time.Time
}

type Session struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}
