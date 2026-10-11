package main

import (
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/db"
)

func newConsentService(database *db.DB, policyVersion string) consent.Service {
	return consent.Service{
		Store:         consent.NewStore(database.Conn()),
		PolicyVersion: policyVersion,
	}
}
