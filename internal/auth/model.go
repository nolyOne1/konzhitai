package auth

import "time"

type RoleName string

const (
	RoleAdmin     RoleName = "admin"
	RoleOperator  RoleName = "operator"
	RoleDeveloper RoleName = "developer"
	RoleViewer    RoleName = "viewer"
)

const (
	PermissionAdmin         = "system.admin"
	PermissionExecute       = "operations.execute"
	PermissionPublishScript = "scripts.publish"
	PermissionRead          = "system.read"
)

func (r RoleName) Allows(permission string) bool {
	permissions := map[RoleName]map[string]bool{
		RoleAdmin: {
			PermissionAdmin:         true,
			PermissionExecute:       true,
			PermissionPublishScript: true,
			PermissionRead:          true,
		},
		RoleOperator: {
			PermissionExecute: true,
			PermissionRead:    true,
		},
		RoleDeveloper: {
			PermissionPublishScript: true,
			PermissionRead:          true,
		},
		RoleViewer: {
			PermissionRead: true,
		},
	}
	return permissions[r][permission]
}

type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Enabled      bool
	Roles        []RoleName
	CreatedAt    time.Time
}

type Session struct {
	ID        string
	UserID    string
	Token     string
	Roles     []RoleName
	ExpiresAt time.Time
	CreatedAt time.Time
}

type StoredSession struct {
	ID        string
	UserID    string
	TokenHash []byte
	ExpiresAt time.Time
	CreatedAt time.Time
}

type Principal struct {
	UserID      string     `json:"user_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Roles       []RoleName `json:"roles"`
}
