package server

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidGroup    = errors.New("服务器组名称须为 1 至 80 个字符")
	ErrGroupNotFound   = errors.New("服务器组不存在，请刷新后重新选择")
	ErrGroupNameExists = errors.New("服务器组名称已存在")
)

type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ServerCount int       `json:"serverCount"`
	CreatedAt   time.Time `json:"createdAt"`
}

type GroupManager interface {
	ListGroups(context.Context) ([]Group, error)
	CreateGroup(context.Context, string) (Group, error)
	RenameGroup(context.Context, string, string) (Group, error)
}

func normalizedGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return "", ErrInvalidGroup
	}
	return name, nil
}
