package org

import (
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"` // "platform", "reseller", "customer"
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Path      string     `json:"path"`
	Status    string     `json:"status"` // "active", "suspended"
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type CreateOrgRequest struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"` // "reseller", "customer" (platform can only be created via bootstrap)
	ParentID *uuid.UUID `json:"parent_id,omitempty"`
}

type UpdateOrgRequest struct {
	Name string `json:"name"`
}

type OrgFilter struct {
	Search   string
	Type     string
	Status   string
	Page     int
	PageSize int
}

type OrgListResult struct {
	Items      []Organization `json:"items"`
	TotalCount int            `json:"total_count"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
}
