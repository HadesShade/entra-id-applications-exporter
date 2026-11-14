package graph

import (
	"context"
	"time"
)

// Plain Go types returned to the rest of the program.
type RawCredential struct {
	KeyID       string    `json:"keyId"`
	DisplayName string    `json:"displayName"`
	ExpiryTime  time.Time `json:"endDateTime"`
}

type RawApplication struct {
	ObjectID            string          `json:"id"`
	AppID               string          `json:"appId"`
	DisplayName         string          `json:"displayName"`
	SignInAudience      string          `json:"signInAudience"`
	PublisherDomain     string          `json:"publisherDomain"`
	CreatedDateTime     time.Time       `json:"createdDateTime"`
	IdentifierURIs      []string        `json:"identifierURIs"`
	PasswordCredentials []RawCredential `json:"passwordCredentials"`
	KeyCredentials      []RawCredential `json:"keyCredentials"`
	Owners              []struct {
		OwnerID string `json:"id"`
	}
}

type Credential struct {
	Name       string
	KeyID      string
	ExpiryTime time.Time
}

type Application struct {
	ObjectID     string
	AppID        string
	Name         string
	Audience     string
	Created      time.Time
	Secrets      []Credential
	Certificates []Credential
	OwnersCount  int
}

// ListApplications returns the app registrations of the tenant.
// Required Graph application permission: Application.Read.All (admin consent).
func (c *Client) ListApplications(ctx context.Context) ([]Application, error) {
	rawApplications, err := listAll[RawApplication](ctx, c, "/applications?$top=999&$expand=owners($select=id)")
	if err != nil {
		return nil, err
	}

	applications := make([]Application, 0, len(rawApplications))

	for _, rawApplication := range rawApplications {
		application := Application{
			ObjectID:    rawApplication.ObjectID,
			AppID:       rawApplication.AppID,
			Name:        rawApplication.DisplayName,
			Audience:    rawApplication.SignInAudience,
			Created:     rawApplication.CreatedDateTime,
			OwnersCount: len(rawApplication.Owners),
		}

		for _, secret := range rawApplication.PasswordCredentials {
			if secret.ExpiryTime.IsZero() {
				continue
			}
			application.Secrets = append(application.Secrets, Credential{
				Name:       secret.DisplayName,
				KeyID:      secret.KeyID,
				ExpiryTime: secret.ExpiryTime,
			})
		}

		for _, certificate := range rawApplication.KeyCredentials {
			if certificate.ExpiryTime.IsZero() {
				continue
			}
			application.Certificates = append(application.Certificates, Credential{
				Name:       certificate.DisplayName,
				KeyID:      certificate.KeyID,
				ExpiryTime: certificate.ExpiryTime,
			})
		}

		applications = append(applications, application)
	}

	return applications, nil
}
